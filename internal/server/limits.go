package server

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxEventsPerRequest = 200
	maxEventsPerAttempt = 5000
	maxAttemptEventData = 1 << 20
	maxSessionEventData = 4 << 20
	maxRunEventData     = 128 << 20
	maxCommandReceipts  = 64
	receiptLifetime     = 24 * time.Hour
	maxRateWindows      = 10000
)

type rateWindow struct {
	expiresAt time.Time
	count     int
}

// rateLimiter bounds the number of distinct keys as well as their request
// rates, so forged source addresses cannot turn rate limiting into a memory
// exhaustion vector.
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]rateWindow
}

func newRateLimiter() *rateLimiter { return &rateLimiter{windows: make(map[string]rateWindow)} }

func (l *rateLimiter) allow(key string, limit int, window time.Duration) (time.Duration, bool) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	v, exists := l.windows[key]
	if !exists && len(l.windows) >= maxRateWindows {
		for k, v := range l.windows {
			// Each quota expires on its own schedule, even when a request
			// with a shorter window triggers capacity cleanup.
			if !now.Before(v.expiresAt) {
				delete(l.windows, k)
			}
		}
		if len(l.windows) >= maxRateWindows {
			return time.Second, false
		}
	}
	if !now.Before(v.expiresAt) {
		v = rateWindow{expiresAt: now.Add(window)}
	}
	if v.count >= limit {
		return v.expiresAt.Sub(now), false
	}
	v.count++
	l.windows[key] = v
	return 0, true
}

func retryAfter(w http.ResponseWriter, wait time.Duration) error {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	return problem(http.StatusTooManyRequests, "too many requests; try again later")
}

func parseTrustedProxies(values []string) ([]netip.Prefix, error) {
	if values == nil {
		values = []string{"127.0.0.1/32", "::1/128"}
	}
	var prefixes []netip.Prefix
	for _, value := range values {
		value = strings.TrimSpace(value)
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			addr, addrErr := netip.ParseAddr(value)
			if addrErr != nil || addr.Zone() != "" {
				return nil, fmt.Errorf("trusted proxy %q must be an IP address or CIDR", value)
			}
			addr = addr.Unmap()
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// The immediate peer must be a configured proxy that overwrites client-supplied
// X-Forwarded-For. Loopback is the default; container gateways are opt-in.
func (s *Server) sourceNetwork(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	ip = ip.Unmap()
	for _, proxy := range s.trustedProxies {
		if proxy.Contains(ip) {
			forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
			if candidate, err := netip.ParseAddr(forwarded); err == nil && candidate.Zone() == "" {
				ip = candidate.Unmap()
			}
			break
		}
	}
	bits := 64
	if ip.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(ip, bits).Masked().String()
}

func publicOperation(path string) string {
	switch {
	case strings.HasSuffix(path, "/join"):
		return "join"
	case strings.HasSuffix(path, "/start"):
		return "start"
	case strings.HasSuffix(path, "/events"):
		return "events"
	case strings.HasSuffix(path, "/finish"):
		return "finish"
	default:
		return ""
	}
}

func (s *Server) limitPublicSource(w http.ResponseWriter, r *http.Request) error {
	op := publicOperation(r.URL.Path)
	if op == "" {
		return nil
	}
	limits := map[string]int{"join": 60, "start": 120, "events": 300, "finish": 120}
	if wait, ok := s.limits.allow("public:source:"+op+":"+s.sourceNetwork(r), limits[op], time.Minute); !ok {
		return retryAfter(w, wait)
	}
	return nil
}

func (s *Server) limitParticipant(w http.ResponseWriter, runID, identity, operation string, replay bool) error {
	var runLimit, participantLimit int
	var runWindow, participantWindow time.Duration
	switch operation {
	case "join":
		runLimit, participantLimit = 240, 60
		runWindow, participantWindow = time.Hour, time.Hour
	case "start":
		runLimit, participantLimit = 500, 30
		runWindow, participantWindow = time.Minute, time.Hour
	case "events":
		runLimit, participantLimit = 2000, 200
		runWindow, participantWindow = time.Minute, time.Minute
	case "finish":
		runLimit, participantLimit = 500, 30
		runWindow, participantWindow = time.Minute, time.Hour
	default:
		return nil
	}
	if wait, ok := s.limits.allow("public:run:"+operation+":"+runID, runLimit, runWindow); !ok {
		return retryAfter(w, wait)
	}
	// Verified receipt replays still consume source and run capacity, but must
	// not use the participant's budget for starting or finishing new tasks.
	if !replay {
		if wait, ok := s.limits.allow("public:participant:"+operation+":"+identity, participantLimit, participantWindow); !ok {
			return retryAfter(w, wait)
		}
	}
	return nil
}
