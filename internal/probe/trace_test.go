package probe

import "testing"

// An ICMP error counts only when it quotes our own echo request.
func TestTraceQuotes(t *testing.T) {
	q := make([]byte, 28)
	q[0] = 0x45 // IPv4, 20-byte header
	q[20] = 8   // echo request
	q[24], q[25], q[26], q[27] = 0x12, 0x34, 0, 5
	if !quotes(q, 0x1234, 5) || quotes(q, 0x1234, 6) || quotes(q, 0x4321, 5) || quotes(q[:20], 0x1234, 5) {
		t.Fatal("quote matching")
	}
	q[20] = 0 // an echo reply quoted: not ours
	if quotes(q, 0x1234, 5) {
		t.Fatal("matched a reply")
	}
}
