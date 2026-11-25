package cluster

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// An agent connection is reachable by anyone who has a token; frames and
// join codes must never crash the reader.

func FuzzReadFrame(f *testing.F) {
	var buf bytes.Buffer
	writeFrame(&buf, msgVote, map[string]any{"round": "r", "monitor": "m"})
	f.Add(buf.Bytes())
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		r := bytes.NewReader(b)
		for i := 0; i < 8; i++ {
			fr, err := readFrame(r, 1<<16)
			if err != nil {
				return
			}
			if len(fr.Data) > 1<<16 {
				t.Fatal("frame larger than the limit")
			}
		}
	})
}

func FuzzDecodeJoin(f *testing.F) {
	f.Add(EncodeJoin(JoinInfo{Server: "https://m:8077", Fingerprint: "sha256:" + string(bytes.Repeat([]byte("ab"), 32)), Code: "nkj_x", Expires: time.Now()}))
	f.Add("nkjoin1.e30")
	f.Fuzz(func(t *testing.T, s string) {
		DecodeJoin(s)
	})
}

func FuzzCompatible(f *testing.F) {
	f.Add("1.0")
	f.Fuzz(func(t *testing.T, v string) {
		compatible(v)
	})
}

var _ = binary.BigEndian
