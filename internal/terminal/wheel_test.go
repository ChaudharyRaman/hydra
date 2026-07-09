package terminal

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

// safeBuf is a concurrency-safe sink for the emulator's child-bound responses.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// drainForked continuously drains the emulator's responses, exactly as a live
// Session does with io.Copy(ptmx, s.em), so writes never block.
func drainForked(e *vt.SafeEmulator) *safeBuf {
	sb := &safeBuf{}
	go io.Copy(sb, e) //nolint:errcheck
	return sb
}

// TestSendMouseWheelForwarding verifies a wheel event is encoded to the child
// only when the child enabled mouse reporting — the basis for letting a
// full-screen app (Claude) scroll itself on the alt screen.
func TestSendMouseWheelForwarding(t *testing.T) {
	// Mouse reporting OFF: nothing should be forwarded.
	e := vt.NewSafeEmulator(40, 10)
	sb := drainForked(e)
	e.Write([]byte("\x1b[?1049h")) // alt screen, but no mouse mode
	if !e.IsAltScreen() {
		t.Fatal("expected alt screen after 1049h")
	}
	e.SendMouse(vt.MouseWheel{X: 5, Y: 5, Button: vt.MouseWheelUp})
	time.Sleep(50 * time.Millisecond)
	if out := sb.String(); out != "" {
		t.Fatalf("wheel forwarded despite mouse reporting off: %q", out)
	}

	// Mouse reporting ON (button-event + SGR): wheel should be encoded.
	e2 := vt.NewSafeEmulator(40, 10)
	sb2 := drainForked(e2)
	e2.Write([]byte("\x1b[?1049h\x1b[?1002h\x1b[?1006h"))
	e2.SendMouse(vt.MouseWheel{X: 5, Y: 5, Button: vt.MouseWheelUp})
	time.Sleep(50 * time.Millisecond)
	out := sb2.String()
	// SGR wheel-up press at 0-based (5,5) -> 1-based (6,6): ESC [ < 64 ; 6 ; 6 M
	if !strings.Contains(out, "\x1b[<64;6;6M") {
		t.Fatalf("wheel not encoded as expected, got %q", out)
	}
}
