//go:build !windows

package app

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// ConfigDir — каталог, в котором лежат identity, config.json и кэши
// (endpoints.json, dht-nodes.dat). Единственный источник этого пути: и сессия,
// и GUI зовут его, чтобы не было двух способов посчитать одно и то же.
//
// Создание сетевого адаптера требует root, то есть обычный запуск — «sudo
// lanmesh». При этом os.UserConfigDir() указал бы на /root/.config: HOME под
// sudo зависит от настроек sudoers, а XDG_CONFIG_HOME обычно вычищается
// env_reset. Для lanmesh это не косметика — виртуальный IP узла выводится из
// PeerID (proto.VirtualIP), который лежит в identity рядом с конфигом. Уехал
// путь — узел получил ДРУГУЮ идентичность и другой адрес в сети, а кэш
// подтверждённых endpoint'ов (internal/netcache) потерял смысл.
//
// Поэтому под root'ом, поднятым через sudo, берём домашнюю папку исходного
// пользователя. Под systemd переменных SUDO_* нет, и путь считается обычным
// образом: юнит задаёт XDG_CONFIG_HOME=/var/lib, и os.UserConfigDir() сам
// отдаёт его — отдельной ветки «мы сервис» в коде не появляется.
func ConfigDir() (string, error) {
	if u, ok := sudoUser(); ok {
		return filepath.Join(u.HomeDir, ".config"), nil
	}
	return os.UserConfigDir()
}

// sudoUser возвращает исходного пользователя, если процесс поднят через sudo.
// user.LookupId без CGO читает /etc/passwd — этого достаточно, и сборку с
// CGO_ENABLED=0 это не ломает.
func sudoUser() (*user.User, bool) {
	if os.Geteuid() != 0 {
		return nil, false
	}
	uid := os.Getenv("SUDO_UID")
	if uid == "" {
		return nil, false
	}
	u, err := user.LookupId(uid)
	if err != nil || u.HomeDir == "" {
		return nil, false
	}
	return u, true
}

// chownToSudoUser передаёт созданный путь исходному пользователю. Без этого
// identity остаётся root-owned с правами 0600 в каталоге 0700 внутри домашней
// папки пользователя, и любой последующий запуск БЕЗ sudo (тот же natcheck)
// упирается в отказ доступа. Best-effort: не смогли — не повод не работать.
//
// Зовётся только для каталога и identity — сознательно. Кэши рядом
// (endpoints.json, dht-nodes.dat) под sudo тоже создаются root'ом, но они
// самовосстанавливаются: чтение под обычным пользователем не удастся, кэш
// прочтётся как пустой, а следующая запись пройдёт (rename требует прав на
// КАТАЛОГ, а он уже принадлежит пользователю) и заменит файл своим. У identity
// такой роскоши нет: не прочитали — сгенерировали новый PeerID, то есть узел
// сменил виртуальный IP, и его перестали видеть по старому адресу.
func chownToSudoUser(path string) {
	u, ok := sudoUser()
	if !ok {
		return
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return
	}
	_ = os.Chown(path, uid, gid)
}
