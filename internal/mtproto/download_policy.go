package mtproto

const (
	adaptiveDownloadPolicyName = "size-tiered-cap2"
	downloadTwoThreadMinBytes  = 1 << 20
)

// adaptiveDownloadThreads controls chunk concurrency inside one active file
// transfer. The Telegram producer and file transfers themselves remain
// sequential. CPU and memory are intentionally not inputs: downloader workers
// wait on network chunks and keep only a bounded 512 KiB part each.
func adaptiveDownloadThreads(size int64) int {
	if size >= downloadTwoThreadMinBytes {
		return 2
	}
	return 1
}
