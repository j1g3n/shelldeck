package bridge

import "log"

// handler_sftp.go
// Small stub for SFTP-specific bridge helpers. The heavy lifting
// (getSFTPClient, upload/download helpers) is implemented in utils.go
// and handlers_fs.go. This file provides a clear place to add
// SFTP-specific orchestration in the future.

func init() {
	log.Println("[handler_sftp] SFTP handler stub initialized")
}
