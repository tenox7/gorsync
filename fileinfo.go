package rsync

import "time"

type FileInfo struct {
	Name     string
	Length   int64
	ModTime  time.Time
	Mode     int32
	Checksum [16]byte
	// LinkTarget is a symbolic link's target, the text the sender's link
	// holds. The sender sends it only with -l (--links); it is empty
	// without -l, and for anything that is no link.
	LinkTarget string
}
