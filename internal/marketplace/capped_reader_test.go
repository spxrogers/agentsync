package marketplace

import (
	"bytes"
	"io"
	"testing"
)

// TestCappedReader_Boundary pins the off-by-one in the tarball cap. The
// previous `remaining <= 0` trip condition failed a payload whose
// decompressed size was exactly the cap, because the tar reader issues a
// trailing probe read after the last data byte. The cap must permit reading
// up to AND including `cap` bytes and only fail on the (cap+1)th.
func TestCappedReader_Boundary(t *testing.T) {
	cases := []struct {
		name    string
		size    int64
		cap     int64
		wantErr bool
	}{
		{"under cap", 99, 100, false},
		{"exactly cap", 100, 100, false},
		{"one over cap", 101, 100, true},
		{"well over cap", 4096, 100, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := bytes.Repeat([]byte("x"), int(tc.size))
			cr := &cappedReader{r: bytes.NewReader(data), remaining: tc.cap, url: "test://t"}
			got, err := io.ReadAll(cr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("size=%d cap=%d: expected cap error, got nil", tc.size, tc.cap)
				}
				return
			}
			if err != nil {
				t.Fatalf("size=%d cap=%d: unexpected error: %v", tc.size, tc.cap, err)
			}
			if int64(len(got)) != tc.size {
				t.Fatalf("size=%d cap=%d: read %d bytes, want %d", tc.size, tc.cap, len(got), tc.size)
			}
		})
	}
}
