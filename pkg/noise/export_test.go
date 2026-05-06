package noise

// SetSendCountForTest pokes the post-handshake send counter so the
// nonce-budget guard can be exercised without actually performing 2^32
// AEAD operations in a unit test. Lives in *_test.go so it is invisible
// to non-test consumers of the package.
func (s *Session) SetSendCountForTest(n uint64) {
	s.sendCount.Store(n)
}
