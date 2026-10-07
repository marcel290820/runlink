package devstack

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v4"
)

// The stand-in keeps the coturn bounds from infra/templates/turnserver.conf.tftpl
// that clients can observe: the relay port range and the allocation quotas.
// Pion's server relays UDP only, as coturn does with no-tcp-relay. Bandwidth
// caps, TLS, and the firewall stay with the real server.
const (
	turnRealm    = "localhost"
	relayMinPort = 49160
	relayMaxPort = 49259
	userQuota    = 4 // allocations per credential username
	totalQuota   = 100
)

// The shared secret has the format C's startup helper accepts.
const maxSecretFile = 4096

var secretFormat = regexp.MustCompile(`^[0-9a-f]{64}$`)

// turnServer is the stand-in for C. Pion's Close stops only the listeners and
// leaves accepted TCP connections and allocation relay sockets to its own
// goroutines, so turnServer tracks both: Close waits until they are released,
// and a listener that stops on its own is reported on failed.
type turnServer struct {
	pion   *turn.Server
	failed chan error

	mu      sync.Mutex
	closing bool
	conns   map[net.Conn]bool // accepted TCP connections
	relays  int               // open relay sockets
}

// startTURN serves STUN and TURN on UDP and TCP at one IPv4 loopback address,
// as coturn listens on C, and accepts coturn's TURN REST credentials signed
// with secret: username "expiry:name", password base64(HMAC-SHA1(secret, username)).
func startTURN(address netip.AddrPort, secret string, logger *slog.Logger) (*turnServer, netip.AddrPort, error) {
	udp, tcp, err := listenUDPAndTCP(address)
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	bound := udp.LocalAddr().(*net.UDPAddr).AddrPort()
	s := &turnServer{failed: make(chan error, 1), conns: map[net.Conn]bool{}}
	relay := func() turn.RelayAddressGenerator {
		return relayPorts{s, &turn.RelayAddressGeneratorPortRange{
			RelayAddress: bound.Addr().AsSlice(), Address: bound.Addr().String(), MinPort: relayMinPort, MaxPort: relayMaxPort,
		}}
	}
	quota := &quota{users: map[string]int{}, reserved: map[string]string{}}
	// Rejections are logged with their reason so credential issuance is easy to
	// debug. Usernames are an expiry and a name; passwords never reach the log.
	rest := turn.LongTermTURNRESTAuthHandler(secret, pionLogger{slog.New(slog.DiscardHandler)})
	s.pion, err = turn.NewServer(turn.ServerConfig{
		Realm: turnRealm,
		AuthHandler: func(username, realm string, source net.Addr) ([]byte, bool) {
			key, ok := rest(username, realm, source)
			if !ok {
				logger.Warn("turn_auth_rejected", "username", username, "reason", "expired or malformed username")
			}
			return key, ok
		},
		QuotaHandler:  quota.allow,
		LoggerFactory: pionLogger{logger},
		EventHandler: turn.EventHandler{
			OnAllocationCreated: quota.created,
			OnAllocationError:   quota.failed,
			OnAllocationDeleted: quota.deleted,
			OnAuth: func(_, _ net.Addr, _, username, _, method string, ok bool) {
				if !ok {
					logger.Warn("turn_auth_rejected", "username", username, "reason", "password does not match the shared secret", "method", method)
				}
			},
		},
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: turnPacketConn{udp, s}, RelayAddressGenerator: relay(), PermissionHandler: loopbackPeer}},
		ListenerConfigs:   []turn.ListenerConfig{{Listener: turnListener{tcp, s}, RelayAddressGenerator: relay(), PermissionHandler: loopbackPeer}},
	})
	if err != nil {
		return nil, netip.AddrPort{}, errors.Join(err, udp.Close(), tcp.Close())
	}
	return s, bound, nil
}

// Close stops the listeners and accepted connections, then waits up to five
// seconds for every allocation to release its relay socket.
func (s *turnServer) Close() error {
	s.mu.Lock()
	s.closing = true
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	err := s.pion.Close()
	for _, conn := range conns {
		_ = conn.Close() // Pion's loop for it then deletes its allocations
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		open := len(s.conns) + s.relays
		s.mu.Unlock()
		if open == 0 {
			return err
		}
		if time.Now().After(deadline) {
			return errors.Join(err, fmt.Errorf("%d TURN connections or relays still open after shutdown", open))
		}
	}
}

// report passes a listener failure to failed unless shutdown caused it.
func (s *turnServer) report(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closing {
		select {
		case s.failed <- err:
		default: // the first failure already stops the stack
		}
	}
}

// Pion's read and accept loops end at their first error.
type turnPacketConn struct {
	net.PacketConn
	s *turnServer
}

func (c turnPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(b)
	if err != nil {
		c.s.report(err)
	}
	return n, addr, err
}

type turnListener struct {
	net.Listener
	s *turnServer
}

func (l turnListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		l.s.report(err)
		return nil, err
	}
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	if l.s.closing {
		return nil, errors.Join(net.ErrClosed, conn.Close())
	}
	tracked := &turnConn{Conn: conn, s: l.s}
	l.s.conns[tracked] = true
	return tracked, nil
}

type turnConn struct {
	net.Conn
	s    *turnServer
	once sync.Once
}

func (c *turnConn) Close() error {
	c.once.Do(func() {
		c.s.mu.Lock()
		delete(c.s.conns, c)
		c.s.mu.Unlock()
	})
	return c.Conn.Close()
}

// relayPorts allocates relays in C's port range and counts the open sockets.
type relayPorts struct {
	s *turnServer
	*turn.RelayAddressGeneratorPortRange
}

func (r relayPorts) AllocatePacketConn(network string, port int) (net.PacketConn, net.Addr, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.closing {
		return nil, nil, net.ErrClosed
	}
	conn, addr, err := r.RelayAddressGeneratorPortRange.AllocatePacketConn(network, port)
	if err != nil {
		return nil, nil, err
	}
	r.s.relays++
	return &relaySocket{PacketConn: conn, release: sync.OnceFunc(func() {
		r.s.mu.Lock()
		r.s.relays--
		r.s.mu.Unlock()
	})}, addr, nil
}

type relaySocket struct {
	net.PacketConn
	release func()
}

func (r *relaySocket) Close() error {
	defer r.release()
	return r.PacketConn.Close()
}

// listenUDPAndTCP binds both protocols to one address. Port zero retries until
// a port is free for both.
func listenUDPAndTCP(address netip.AddrPort) (*net.UDPConn, *net.TCPListener, error) {
	for attempt := 1; ; attempt++ {
		udp, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(address))
		if err != nil {
			return nil, nil, err
		}
		tcp, err := net.ListenTCP("tcp", net.TCPAddrFromAddrPort(udp.LocalAddr().(*net.UDPAddr).AddrPort()))
		if err == nil {
			return udp, tcp, nil
		}
		_ = udp.Close() // the listen error is the one to report
		if address.Port() != 0 || attempt == 10 {
			return nil, nil, err
		}
	}
}

// loopbackPeer stands in for C's destination policy. Every peer runs on this
// machine, so relays reach loopback addresses and nothing else.
func loopbackPeer(_ net.Addr, peer net.IP) bool { return peer.IsLoopback() }

// quota matches coturn's user-quota and total-quota. allow reserves a slot for
// the allocation it admits; the allocation keeps it until deleted, and a failed
// allocation releases it. Pion handles one request at a time per client
// address, so the address identifies a pending reservation.
type quota struct {
	mu       sync.Mutex
	users    map[string]int
	total    int
	reserved map[string]string // client address to username, until created or failed
}

func clientKey(client net.Addr) string { return client.Network() + " " + client.String() }

func (q *quota) allow(username, _ string, client net.Addr) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.total >= totalQuota || q.users[username] >= userQuota {
		return false
	}
	q.users[username]++
	q.total++
	q.reserved[clientKey(client)] = username
	return true
}

func (q *quota) created(client, _ net.Addr, _, _, _ string, _ net.Addr, _ int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.reserved, clientKey(client))
}

// failed runs for any request that fails; only a pending reservation matters.
func (q *quota) failed(client, _ net.Addr, _, _ string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if username, ok := q.reserved[clientKey(client)]; ok {
		delete(q.reserved, clientKey(client))
		q.release(username)
	}
}

func (q *quota) deleted(_, _ net.Addr, _, username, _ string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.release(username)
}

func (q *quota) release(username string) {
	if q.users[username]--; q.users[username] <= 0 {
		delete(q.users, username)
	}
	q.total--
}

// pionLogger passes Pion's warnings and errors into the stack's structured log
// and drops its debug output.
type pionLogger struct{ log *slog.Logger }

func (l pionLogger) NewLogger(string) logging.LeveledLogger { return l }
func (l pionLogger) Warn(msg string)                        { l.log.Warn("turn_warning", "detail", msg) }
func (l pionLogger) Warnf(format string, args ...any)       { l.Warn(fmt.Sprintf(format, args...)) }
func (l pionLogger) Error(msg string)                       { l.log.Error("turn_error", "detail", msg) }
func (l pionLogger) Errorf(format string, args ...any)      { l.Error(fmt.Sprintf(format, args...)) }
func (pionLogger) Info(string)                              {}
func (pionLogger) Infof(string, ...any)                     {}
func (pionLogger) Debug(string)                             {}
func (pionLogger) Debugf(string, ...any)                    {}
func (pionLogger) Trace(string)                             {}
func (pionLogger) Tracef(string, ...any)                    {}

// loadOrCreateSecret returns the TURN shared secret at path, generating a new
// private one when the file does not exist. It keeps across restarts, as the
// secret on B and C does, so issued credentials stay valid. The file must be a
// small private regular file; symlinks are not followed and a FIFO cannot block.
func loadOrCreateSecret(path string) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return createSecret(path)
	}
	if err != nil {
		return "", fmt.Errorf("cannot read the TURN secret: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("cannot read the TURN secret: %w", err)
	}
	switch {
	case !info.Mode().IsRegular():
		return "", errors.New("the TURN secret must be a regular file")
	case info.Mode().Perm()&0o077 != 0:
		return "", errors.New("the TURN secret file must be private (0600)")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSecretFile+1))
	if err != nil {
		return "", fmt.Errorf("cannot read the TURN secret: %w", err)
	}
	secret := strings.TrimSpace(string(data)) // as C's startup helper reads it
	if len(data) > maxSecretFile || !secretFormat.MatchString(secret) {
		return "", errors.New("the TURN secret must be 64 lowercase hex characters")
	}
	return secret, nil
}

func createSecret(path string) (string, error) {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret) // never fails; see crypto/rand
	encoded := hex.EncodeToString(secret)
	// O_EXCL also refuses an existing symlink at path.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("cannot create the TURN secret: %w", err)
	}
	_, err = file.WriteString(encoded + "\n")
	if err = errors.Join(err, file.Close()); err != nil {
		return "", fmt.Errorf("cannot write the TURN secret: %w", err)
	}
	return encoded, nil
}
