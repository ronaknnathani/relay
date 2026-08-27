package ui

import "testing"

func TestOpenBrowserUsesPlatformOpener(t *testing.T) {
	for _, test := range []struct {
		goos string
		want string
	}{
		{goos: "darwin", want: "open"},
		{goos: "linux", want: "xdg-open"},
	} {
		t.Run(test.goos, func(t *testing.T) {
			var command, argument string
			err := openBrowser("http://127.0.0.1:1234", test.goos, func(name string, args ...string) error {
				command = name
				argument = args[0]
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if command != test.want || argument != "http://127.0.0.1:1234" {
				t.Fatalf("opener = %q %q", command, argument)
			}
		})
	}
}
