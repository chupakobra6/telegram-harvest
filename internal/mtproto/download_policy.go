package mtproto

const (
	adaptiveDownloadPolicyName = "size-tiered-cap2"
	downloadTwoThreadMinBytes  = 1 << 20
)

// adaptiveDownloadThreads controls how many of the coordinator's two global
// Telegram chunk slots one file occupies. CPU and memory are intentionally not
// inputs: downloader workers wait on network chunks and keep only a bounded
// 512 KiB part each.
func adaptiveDownloadThreads(size int64) int {
	if size >= downloadTwoThreadMinBytes {
		return 2
	}
	return 1
}
