//go:build windows

package main

import "fmt"

// pause держит окно открытым: при двойном клике по exe консоль исчезает сразу
// после выхода процесса, и вердикт прочитать не успевают.
func pause() {
	fmt.Print("\nНажми Enter, чтобы закрыть окно...")
	fmt.Scanln()
}
