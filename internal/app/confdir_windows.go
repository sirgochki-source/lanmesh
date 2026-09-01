//go:build windows

package app

import "os"

// ConfigDir — каталог, в котором лежат identity, config.json и кэши
// (endpoints.json, dht-nodes.dat). Единственный источник этого пути: и сессия,
// и GUI зовут его, чтобы не было двух способов посчитать одно и то же.
//
// На Windows это просто %AppData%: аналога sudo, подменяющего домашнюю папку
// процесса, здесь нет — GUI перезапускается через UAC под тем же пользователем.
func ConfigDir() (string, error) { return os.UserConfigDir() }

// chownToSudoUser на Windows не делает ничего: владельца менять не нужно и
// некому. Существует, чтобы вызывающий код был общим для обеих платформ.
func chownToSudoUser(string) {}
