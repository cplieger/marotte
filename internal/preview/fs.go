package preview

import (
	"errors"

	"golang.org/x/sys/unix"
)

var (
	// errResolveUnsupported means the kernel cannot do a no-follow resolution, so
	// the request is refused rather than served by a weaker walk.
	errResolveUnsupported = errors.New("openat2 is unavailable")
	// The request fails closed instead of spinning.
	errResolveBusy = errors.New("openat2 kept racing a rename")
)

// RESOLVE_BENEATH answers EAGAIN when a concurrent rename stops the kernel proving containment, so
// a tree under rename churn could otherwise hold the handler forever.
const maxResolveAttempts = 8

// openat2 is unix.Openat2, reassignable so a test can drive the kernel's
// transient and unsupported answers.
var openat2 = unix.Openat2

// openBeneath opens rel relative to dirfd in ONE kernel call that never leaves
// dirfd's tree and follows no symlink, magic link or "..", so there is no
// check-then-open window for a concurrent rename to widen.
// See openat2(2), RESOLVE_BENEATH / RESOLVE_NO_SYMLINKS.
func openBeneath(dirfd int, rel string, flags int) (int, error) {
	how := unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC), //nolint:gosec // G115: open flags are small positive constants.
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	for range maxResolveAttempts {
		fd, err := openat2(dirfd, rel, &how)
		switch {
		case err == nil:
			return fd, nil
		case errors.Is(err, unix.EINTR), errors.Is(err, unix.EAGAIN):
			continue
		case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EPERM):
			return -1, errResolveUnsupported
		default:
			return -1, err
		}
	}
	return -1, errResolveBusy
}

// "" is dirfd itself.
func openDir(dirfd int, rel string) (int, error) {
	if rel == "" {
		rel = "."
	}
	return openBeneath(dirfd, rel, unix.O_RDONLY|unix.O_DIRECTORY)
}

// O_NONBLOCK keeps a FIFO planted at the name from blocking the open; anything but a regular file
// is closed and refused.
func openRegular(dirfd int, rel string) (int, unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := openBeneath(dirfd, rel, unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return -1, st, err
	}
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return -1, st, errNotRegular
	}
	return fd, st, nil
}

var errNotRegular = errors.New("not a regular file")
