package mtproto

import "testing"

func TestAdaptiveDownloadThreadsUsesBoundedSizeTiers(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want int
	}{
		{name: "unknown", size: 0, want: 1},
		{name: "small", size: downloadTwoThreadMinBytes - 1, want: 1},
		{name: "medium boundary", size: downloadTwoThreadMinBytes, want: 2},
		{name: "large stays capped", size: 256 << 20, want: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := adaptiveDownloadThreads(test.size); got != test.want {
				t.Fatalf("adaptiveDownloadThreads(%d) = %d, want %d", test.size, got, test.want)
			}
		})
	}
}
