package display

import "fmt"

const asciiBanner = `   _         _  _                       _
  (_)_  _ __| || |_ _  _ _ _  _ _  ___| |
  | | || (_-<  _|  _| || | ' \| ' \/ -_) |
 _/ |\_,_/__/\__|\__|\_,_|_||_|_||_\___|_|
|__/`

func PrintBanner(subdomain, url, localTarget string) {
	subdomain = sanitize(subdomain)
	url = sanitize(url)
	localTarget = sanitize(localTarget)

	fmt.Fprintln(output)

	if IsTerminal() && TerminalWidth() >= 60 {
		fmt.Fprintln(output, asciiBanner)
	} else {
		fmt.Fprintln(output, "  justtunnel")
	}

	fmt.Fprintln(output)
	colorCyan.Fprintf(output, "  %-14s", "Forwarding:")
	colorWhite.Fprintf(output, " %s", url)
	colorDim.Fprintf(output, " -> ")
	colorWhite.Fprintf(output, "%s\n", localTarget)
	colorCyan.Fprintf(output, "  %-14s", "Subdomain:")
	colorWhite.Fprintf(output, " %s\n", subdomain)
	fmt.Fprintln(output)
}
