package keymap

import "testing"

func TestPhysicalPositions(t *testing.T) {
	for _, c := range []struct {
		scan, usage uint16
		ext         bool
	}{{0x1e, 4, false}, {0x1d, 0xe0, false}, {0x1d, 0xe4, true}, {0x1c, 0x28, false}, {0x1c, 0x58, true}, {0x48, 0x60, false}, {0x48, 0x52, true}} {
		if k, ok := Set1(c.scan, c.ext); !ok || k != c.usage {
			t.Fatal(c, k, ok)
		}
	}
	if k, ok := Linux(97); !ok || k != 0xe4 {
		t.Fatal(k, ok)
	}
	if _, ok := Linux(65535); ok {
		t.Fatal("unknown key mapped")
	}
}
