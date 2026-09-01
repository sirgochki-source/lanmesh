//go:build !windows

package main

// pause на Linux не нужна: natcheck запускают из терминала, который никуда не
// девается, а лишнее ожидание ввода ломало бы запуск из скрипта.
func pause() {}
