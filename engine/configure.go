package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const manifestPath = confDir + "/processes.json"

func (e nodes) Configure(ctx context.Context, request requests.NodeConfigure) (responses.NodeConfigure, error) {
	line := os.Getenv("DATABASIFY_LINE")
	request, err := e.withTunedSettings(request)
	if err != nil {
		return responses.NodeConfigure{}, err
	}
	files, err := filesOf(request, line)
	if err != nil {
		return responses.NodeConfigure{}, err
	}
	manifest, err := manifestOf(request, line)
	if err != nil {
		return responses.NodeConfigure{}, err
	}
	changed, err := e.writeAll(files)
	if err != nil {
		return responses.NodeConfigure{}, err
	}
	if err := e.ensurePoolsFile(); err != nil {
		return responses.NodeConfigure{}, err
	}
	if err := e.removeOtherModeFiles(request); err != nil {
		return responses.NodeConfigure{}, err
	}
	manifestChanged, err := e.write(nodeFile{path: manifestPath, content: string(manifest), mode: 0o640})
	if err != nil {
		return responses.NodeConfigure{}, err
	}
	if manifestChanged {
		if err := e.supervisor.Do("daemon-reload"); err != nil {
			return responses.NodeConfigure{}, err
		}
	}
	if err := e.prepare(ctx, request, line); err != nil {
		return responses.NodeConfigure{}, err
	}

	return responses.NodeConfigure{Changed: changed || manifestChanged}, nil
}

func (e Engine) writeAll(files []nodeFile) (bool, error) {
	changed := false
	for _, file := range files {
		written, err := e.write(file)
		if err != nil {
			return false, err
		}
		changed = changed || written
	}

	return changed, nil
}

func (e Engine) write(file nodeFile) (bool, error) {
	path := e.path(file.path)
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, []byte(file.content)) {
		return false, nil
	}
	if err := e.ensureDirectory(filepath.Dir(path)); err != nil {
		return false, err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(file.content), file.mode); err != nil {
		return false, err
	}
	if err := e.own(temporary); err != nil {
		return false, err
	}

	return true, os.Rename(temporary, path)
}

func (e Engine) ensureDirectory(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return err
	}

	return e.own(path)
}

func (e Engine) own(path string) error {
	err := os.Lchown(path, postgresUID, postgresUID)
	if errors.Is(err, os.ErrPermission) && os.Geteuid() != 0 {
		return nil
	}

	return err
}

func (e Engine) path(path string) string {
	return filepath.Join(e.root, strings.TrimPrefix(path, defaultRoot))
}

func (e Engine) removeOtherModeFiles(request requests.NodeConfigure) error {
	stale := []string{}
	if request.Clustered {
		stale = append(stale, "postgresql.conf", "pg_hba.conf")
	}
	if request.Archive == nil {
		stale = append(stale, "pgbackrest.conf")
	}
	for _, name := range stale {
		if err := os.Remove(e.path(confDir + "/" + name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	return nil
}

func (e Engine) prepare(ctx context.Context, request requests.NodeConfigure, line string) error {
	if err := e.directories(0o700, "data", "run"); err != nil {
		return err
	}
	if err := e.directories(0o750, "archive", "archive/log", "archive/spool", "archive/lock", "archive/state"); err != nil {
		return err
	}
	if !request.Clustered {
		return e.initialise(ctx, line)
	}
	if request.ReadOnly {
		return nil
	}
	if err := e.directories(0o700, "etcd"); err != nil {
		return err
	}

	return e.repairEtcdMembership(ctx, request)
}

func (e Engine) directories(mode os.FileMode, names ...string) error {
	for _, name := range names {
		path := filepath.Join(e.root, name)
		if err := os.MkdirAll(path, mode); err != nil {
			return err
		}
		if err := e.own(path); err != nil {
			return err
		}
	}

	return nil
}

func (e Engine) initialise(ctx context.Context, line string) error {
	if info, err := os.Stat(filepath.Join(e.root, "data", "PG_VERSION")); err == nil && info.Size() > 0 {
		return nil
	}
	_, err := e.tools.Run(ctx, "/usr/lib/postgresql/"+line+"/bin/initdb", toolRun{
		args: []string{"-D", defaultRoot + "/data", "--encoding=UTF8", "--data-checksums", "--auth-local=peer", "--auth-host=scram-sha-256"},
	})

	return err
}

func (e Engine) repairEtcdMembership(ctx context.Context, request requests.NodeConfigure) error {
	endpoint := e.answeringEtcd(ctx, request.Members, request.Ip)
	if endpoint == "" {
		return nil
	}
	members, err := e.etcd(ctx, endpoint, "member", "list")
	if err != nil {
		return nil
	}
	listed := memberIDOf(members, request.Ip)
	hasData := e.exists("etcd/member")
	switch {
	case listed != "" && !hasData:
		if _, err := e.etcd(ctx, endpoint, "member", "remove", listed); err != nil {
			return err
		}

		return e.registerAgain(ctx, endpoint, request.Ip)
	case listed == "" && hasData:
		if err := e.supervisor.Do("stop", etcdUnit); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(e.root, "etcd", "member")); err != nil {
			return err
		}

		return e.registerAgain(ctx, endpoint, request.Ip)
	case listed == "":
		return e.registerAgain(ctx, endpoint, request.Ip)
	}

	return nil
}

func (e Engine) registerAgain(ctx context.Context, endpoint string, ip string) error {
	added, err := e.etcd(ctx, endpoint, "member", "add", ip, "--peer-urls=http://"+ip+":"+etcdPeerPort, "--learner")
	if err != nil {
		return err
	}
	lines := []string{}
	for _, line := range strings.Split(added, "\n") {
		if strings.HasPrefix(line, "ETCD_") {
			lines = append(lines, line)
		}
	}
	_, err = e.write(nodeFile{path: confDir + "/etcd-initial.env", content: strings.Join(lines, "\n") + "\n", mode: 0o640})

	return err
}

func (e Engine) answeringEtcd(ctx context.Context, members []dtos.Member, own string) string {
	for _, member := range members {
		if !member.Voting || member.Ip == own {
			continue
		}
		endpoint := "http://" + member.Ip + ":" + etcdClientPort
		if _, err := e.etcd(ctx, endpoint, "member", "list"); err == nil {
			return endpoint
		}
	}

	return ""
}

func (e Engine) etcd(ctx context.Context, endpoint string, args ...string) (string, error) {
	return e.tools.Run(ctx, "etcdctl", toolRun{args: append([]string{"--endpoints=" + endpoint, "--command-timeout=10s"}, args...), timeout: etcdTimeout})
}

func memberIDOf(members string, ip string) string {
	peer := "http://" + ip + ":" + etcdPeerPort
	for _, line := range strings.Split(members, "\n") {
		fields := strings.Split(line, ", ")
		if len(fields) >= 4 && fields[3] == peer {
			return fields[0]
		}
	}

	return ""
}

func (e Engine) exists(name string) bool {
	_, err := os.Stat(filepath.Join(e.root, name))

	return err == nil
}
