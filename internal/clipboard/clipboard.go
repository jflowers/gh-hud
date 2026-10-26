// Package clipboard provides macOS clipboard and browser helpers.
package clipboard

import "os/exec"

// Copy puts text on the pasteboard via pbcopy.
func Copy(text string) error {
	cmd := exec.Command("pbcopy")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := in.Write([]byte(text)); err != nil {
		return err
	}
	if err := in.Close(); err != nil {
		return err
	}
	return cmd.Wait()
}

// OpenURL opens a URL in the default browser.
func OpenURL(url string) error {
	return exec.Command("open", url).Start()
}
