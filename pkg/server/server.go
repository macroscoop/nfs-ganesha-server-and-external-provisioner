/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"

	"github.com/golang/glog"
)

var defaultGaneshaConfigContents = []byte(`
###################################################
#
# EXPORT
#
# To function, all that is required is an EXPORT
#
# Define the absolute minimal export
#
###################################################

EXPORT {

	# Export Id (mandatory, each EXPORT must have a unique Export_Id)
	Export_Id = 0;
	# Exported path (mandatory)
	Path = /;
	# Pseudo Path (required for NFS v4)
	Pseudo = /;
	Protocols = 4;
	Access_Type = MDONLY_RO;
	Filesystem_Id = 152.152;

	FSAL {

		Name = PSEUDO;

	}
}

NFS_Core_Param
{
	MNT_Port = 20048;
	NLM_Port = 32803;
	fsid_device = true;
	allow_set_io_flusher_fail = true;
}

NFSV4
{
	Grace_Period = 90;
}
`)

// Setup sets up various prerequisites and settings for the server. If an error
// is encountered at any point it returns it instantly
func Setup(ganeshaConfig string, gracePeriod uint, fsidDevice bool, enableNFSv3 bool) error {
	// rpcbind (the RPC portmapper) is required by ganesha even for an
	// NFSv4-only server: ganesha registers every RPC program it serves
	// (including NFS_V4 itself and RQUOTA) with the local portmapper at
	// startup, and a registration failure is fatal. So always start it.
	//
	// Start rpcbind if it is not started yet
	cmd := exec.Command("/usr/sbin/rpcinfo", "127.0.0.1")
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("/usr/sbin/rpcbind", "-w")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("Starting rpcbind failed with error: %v, output: %s", err, out)
		}
	}

	// rpc.statd (NSM) only serves NFSv3's NLM locking, so skip it when
	// NFSv3 is disabled. Combined with NFS_Protocols=4 in the ganesha
	// config (see setNFSProtocols), the server then exposes no v3/NLM/mount
	// RPC services at all, only NFSv4 over 2049.
	if enableNFSv3 {
		cmd = exec.Command("/usr/sbin/rpc.statd", "--port", "662")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("rpc.statd failed with error: %v, output: %s", err, out)
		}
	}

	// Start dbus, needed for ganesha dynamic exports
	cmd = exec.Command("dbus-daemon", "--system", "--nopidfile")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dbus-daemon failed with error: %v, output: %s", err, out)
	}

	err := setRlimitNOFILE()
	if err != nil {
		glog.Warningf("Error setting RLIMIT_NOFILE, there may be 'Too many open files' errors later: %v", err)
	}

	// Use defaultGaneshaConfigContents if the ganeshaConfig doesn't exist yet
	if _, err = os.Stat(ganeshaConfig); os.IsNotExist(err) {
		err = ioutil.WriteFile(ganeshaConfig, defaultGaneshaConfigContents, 0600)
		if err != nil {
			return fmt.Errorf("error writing ganesha config %s: %v", ganeshaConfig, err)
		}
	}
	err = setGracePeriod(ganeshaConfig, gracePeriod)
	if err != nil {
		return fmt.Errorf("error setting grace period to ganesha config: %v", err)
	}
	err = setFsidDevice(ganeshaConfig, fsidDevice)
	if err != nil {
		return fmt.Errorf("error setting fsid device to ganesha config: %v", err)
	}
	if enableNFSv3 {
		err = setNlmPort(ganeshaConfig)
		if err != nil {
			return fmt.Errorf("error setting NLM port to ganesha config: %v", err)
		}
	}
	err = setNFSProtocols(ganeshaConfig, enableNFSv3)
	if err != nil {
		return fmt.Errorf("error setting NFS protocols in ganesha config: %v", err)
	}

	return nil
}

// setNFSProtocols pins which NFS protocol versions ganesha serves, by writing
// (or clearing) an NFS_Protocols line in the NFS_Core_Param block of the
// persisted ganesha config. This must run on every startup, not just when the
// config is first created, because the config lives on a persisted volume and
// the desired protocol set is governed by the -enable-nfs-v3 flag, which can
// differ from when the file was written.
//
// When NFSv3 is disabled we set "NFS_Protocols = 4;" so ganesha registers and
// serves only NFSv4. Leaving v3 enabled (the default) makes ganesha also try
// to register the v3/mount/NLM programs with the portmapper. When NFSv3 is
// enabled we remove any line we previously added, restoring ganesha's default
// (3,4) protocol set.
func setNFSProtocols(ganeshaConfig string, enableNFSv3 bool) error {
	read, err := ioutil.ReadFile(ganeshaConfig)
	if err != nil {
		return err
	}

	re := regexp.MustCompile(`(?m)^\s*NFS_Protocols = [0-9,]+;\n`)
	stripped := re.ReplaceAll(read, []byte(""))

	if !enableNFSv3 {
		// Insert "NFS_Protocols = 4;" as the first line inside NFS_Core_Param.
		blockRe := regexp.MustCompile(`(?m)^(NFS_Core_Param\s*\n{\n)`)
		if blockRe.Match(stripped) {
			stripped = blockRe.ReplaceAll(stripped, []byte("${1}\tNFS_Protocols = 4;\n"))
		}
	}

	return ioutil.WriteFile(ganeshaConfig, stripped, 0600)
}

// Run : run the NFS server in the foreground until it exits
// Ideally, it should never exit when run in foreground mode
// We force foreground to allow the provisioner process to restart
// the server if it crashes - daemonization prevents us from using Wait()
// for this purpose
func Run(ganeshaLog, ganeshaPid, ganeshaConfig string) error {
	// Start ganesha.nfsd
	glog.Infof("Running NFS server!")
	cmd := exec.Command("ganesha.nfsd", "-F", "-L", ganeshaLog, "-p", ganeshaPid, "-f", ganeshaConfig)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ganesha.nfsd failed with error: %v, output: %s", err, out)
	}

	return nil
}

func setRlimitNOFILE() error {
	var rlimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlimit)
	if err != nil {
		return fmt.Errorf("error getting RLIMIT_NOFILE: %v", err)
	}
	glog.Infof("starting RLIMIT_NOFILE rlimit.Cur %d, rlimit.Max %d", rlimit.Cur, rlimit.Max)
	rlimit.Max = 1024 * 1024
	rlimit.Cur = 1024 * 1024
	err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rlimit)
	if err != nil {
		return err
	}
	err = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlimit)
	if err != nil {
		return fmt.Errorf("error getting RLIMIT_NOFILE: %v", err)
	}
	glog.Infof("ending RLIMIT_NOFILE rlimit.Cur %d, rlimit.Max %d", rlimit.Cur, rlimit.Max)
	return nil
}

func setFsidDevice(ganeshaConfig string, fsidDevice bool) error {
	newLine := fmt.Sprintf("fsid_device = %t;", fsidDevice)

	re := regexp.MustCompile("fsid_device = (true|false);")

	read, err := ioutil.ReadFile(ganeshaConfig)
	if err != nil {
		return err
	}

	oldLine := re.Find(read)

	if oldLine == nil {
		// fsid_device line not there, append it after MNT_Port
		re := regexp.MustCompile("MNT_Port = 20048;")

		mntPort := re.Find(read)

		block := "MNT_Port = 20048;\n" +
			"\t" + newLine

		replaced := strings.Replace(string(read), string(mntPort), block, -1)
		err = ioutil.WriteFile(ganeshaConfig, []byte(replaced), 0)
		if err != nil {
			return err
		}
	} else {
		// fsid_device there, just replace it
		replaced := strings.Replace(string(read), string(oldLine), newLine, -1)
		err = ioutil.WriteFile(ganeshaConfig, []byte(replaced), 0)
		if err != nil {
			return err
		}
	}

	return nil
}

func setNlmPort(ganeshaConfig string) error {
	newLine := "NLM_Port = 32803;"

	re := regexp.MustCompile("NLM_Port = ([0-9]+);")

	read, err := ioutil.ReadFile(ganeshaConfig)
	if err != nil {
		return err
	}

	oldLine := re.Find(read)

	if oldLine == nil {
		// fsid_device line not there, append it after MNT_Port
		re := regexp.MustCompile("MNT_Port = 20048;")

		mntPort := re.Find(read)

		block := "MNT_Port = 20048;\n" +
			"\t" + newLine

		replaced := strings.Replace(string(read), string(mntPort), block, -1)
		err = ioutil.WriteFile(ganeshaConfig, []byte(replaced), 0)
		if err != nil {
			return err
		}
	}

	return nil
}

func setGracePeriod(ganeshaConfig string, gracePeriod uint) error {
	if gracePeriod > 180 {
		return fmt.Errorf("grace period cannot be greater than 180")
	}

	newLine := fmt.Sprintf("Grace_Period = %d;", gracePeriod)

	re := regexp.MustCompile("Grace_Period = [0-9]+;")

	read, err := ioutil.ReadFile(ganeshaConfig)
	if err != nil {
		return err
	}

	oldLine := re.Find(read)

	var file *os.File
	if oldLine == nil {
		// Grace_Period line not there, append the whole NFSV4 block.
		file, err = os.OpenFile(ganeshaConfig, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()

		block := "\nNFSV4\n{\n" +
			"\t" + newLine + "\n" +
			"}\n"

		if _, err = file.WriteString(block); err != nil {
			return err
		}
		file.Sync()
	} else {
		// Grace_Period line there, just replace it
		replaced := strings.Replace(string(read), string(oldLine), newLine, -1)
		err = ioutil.WriteFile(ganeshaConfig, []byte(replaced), 0)
		if err != nil {
			return err
		}
	}

	return nil
}

// Stop stops the NFS server.
func Stop() {
	// /bin/dbus-send --system   --dest=org.ganesha.nfsd --type=method_call /org/ganesha/nfsd/admin org.ganesha.nfsd.admin.shutdown
}
