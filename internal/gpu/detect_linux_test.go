//go:build linux

package gpu

import "testing"

func TestParseLSPCI(t *testing.T) {
	raw := `
0000:00:02.0 VGA compatible controller [0300]: Intel Corporation Device [8086:46a6] (rev 0c)
0000:01:00.0 3D controller [0302]: NVIDIA Corporation AD102 [GeForce RTX 4090] [10de:2684] (rev a1)
0000:00:01.0 VGA compatible controller [0300]: Microsoft Corporation Basic Display Adapter [1414:5353]
`
	gpus := parseLSPCI(raw)
	if len(gpus) != 2 {
		t.Fatalf("got %d gpus: %+v", len(gpus), gpus)
	}
	if gpus[1].Vendor != VendorNVIDIA {
		t.Errorf("vendor=%s name=%s", gpus[1].Vendor, gpus[1].Name)
	}
	if gpus[0].Vendor != VendorIntel {
		t.Errorf("intel vendor=%s", gpus[0].Vendor)
	}
}
