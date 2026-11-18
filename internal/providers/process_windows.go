//go:build windows

package providers

import "golang.org/x/sys/windows"

// processAlive reports whether pid still exists. Windows has no signal 0, so
// the process object is opened and its exit code checked instead.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
