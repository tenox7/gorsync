package rsync

import "time"

type FileInfo struct {
	Name     string
	Length   int64
	ModTime  time.Time
	Mode     int32
	Checksum [16]byte
}
