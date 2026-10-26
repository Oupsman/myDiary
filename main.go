// Command mydiary is a local, encrypted diary with a GTK 4 interface.
package main

import (
	"os"

	"github.com/oupsman/mydiary/internal/ui"
)

func main() {
	os.Exit(ui.Run(os.Args))
}
