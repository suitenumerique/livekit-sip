//go:build debug_ui

package pipeline

import (
	"fmt"
	"time"

	"github.com/go-gst/go-glib/glib"
)

func init() {
	go func() {
		done := make(chan time.Time)
		defer close(done)

		go func() {
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()

			for range ticker.C {
				if _, err := glib.IdleAdd(func() {
					done <- time.Now()
				}); err != nil {
					fmt.Println("Error adding idle callback:", err)
				}
			}
		}()

		for {
			select {
			case t := <-done:
				fmt.Println("Main loop is alive at", t)
			case <-time.After(5 * time.Second):
				fmt.Println("Main loop is not responding")
			}
		}
	}()
}
