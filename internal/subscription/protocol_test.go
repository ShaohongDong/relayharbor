package subscription_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitee.com/kenny3755/gost_deploy/internal/config"
	"gitee.com/kenny3755/gost_deploy/internal/native"
	"gitee.com/kenny3755/gost_deploy/internal/socks"
	"gitee.com/kenny3755/gost_deploy/internal/subscription"
	"gitee.com/kenny3755/gost_deploy/internal/supervisor"
	"gitee.com/kenny3755/gost_deploy/internal/testkit"
)

func TestAnyTLSProtocol(t *testing.T) {
	arch := runtime.GOARCH
	root := os.Getenv("GOST_ARTIFACTS")
	if root == "" {
		root = filepath.Join(testkit.Root(), ".cache/releases")
	}
	if _, e := os.Stat(filepath.Join(root, "sing-box-1.14.3-linux-"+arch+".tar.gz")); e != nil {
		if os.Getenv("GOST_TEST_REAL") == "1" {
			t.Fatal(e)
		}
		t.Skip("make fetch required")
	}
	binary := testkit.SingBox(t, arch, root)
	gost := testkit.GOST(t)
	dir := t.TempDir()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	keyder, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyder}), 0600)
	cfg := config.Initial(testkit.Addr(t), true)
	options := native.Options{Gateway: testkit.Addr(t), Auth: config.Auth{Username: "internal", Password: config.Secret()}}
	r := supervisor.New(supervisor.Input{Config: cfg, Options: options, Binary: gost, Revision: "anytls-test"}, t.TempDir())
	r.Command = testkit.Command
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	testkit.Wait(t, 8*time.Second, func() bool { return r.Status().Ready })
	addr := testkit.Addr(t)
	_, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	user := subscription.User{ID: "client", Name: "test: \"设备\"", Port: port, Enabled: true, Password: config.Secret(), Token: config.Secret()}
	path := filepath.Join(dir, "server.json")
	os.WriteFile(path, config.Bytes(subscription.Native(user, options.Gateway, options.Auth, certPath, keyPath)), 0600)
	testkit.StartSingBox(t, binary, path, addr)
	clientArch := "amd64-compatible"
	if arch == "arm64" {
		clientArch = "arm64"
	}
	archive := filepath.Join(root, "mihomo-linux-"+clientArch+"-v1.19.32.gz")
	yaml := subscription.YAML("localhost", user, false)
	client := testkit.Mihomo(t, archive, yaml, certPath)
	echo := testkit.Echo(t)
	c, e := testkit.ClientConnect(client, echo)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = testkit.Payload(c); e != nil {
		t.Fatal(e)
	}
	// Override only the advertised UDP capability to exercise server-side rejection
	// even though the existing direct scheduler permits UDP.
	udpClient := testkit.Mihomo(t, archive, []byte(strings.Replace(string(yaml), "udp: false", "udp: true", 1)), certPath)
	udpTarget := testkit.UDP(t)
	control, e := net.DialTimeout("tcp", udpClient, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer control.Close()
	control.SetDeadline(time.Now().Add(5 * time.Second))
	control.Write([]byte{5, 1, 0})
	response := make([]byte, 2)
	if _, e = control.Read(response); e != nil {
		t.Fatal(e)
	}
	reply, e := socks.Request(control, 3, socks.Address("0.0.0.0", 0))
	if e != nil {
		t.Fatal("client UDP association", e)
	}
	if len(reply) != 10 || reply[3] != 1 {
		t.Fatal("unexpected UDP reply")
	}
	udpAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(reply[8])<<8|int(reply[9])))
	udp, e := net.Dial("udp", udpAddr)
	if e != nil {
		t.Fatal(e)
	}
	defer udp.Close()
	udp.SetDeadline(time.Now().Add(time.Second))
	packet := append(append([]byte{0, 0, 0}, socks.Target(udpTarget)...), []byte("must-not-pass")...)
	udp.Write(packet)
	if _, e = udp.Read(make([]byte, 512)); e == nil {
		t.Fatal("AnyTLS UDP unexpectedly forwarded")
	}
	bad := user
	bad.Password = config.Secret()
	badClient := testkit.Mihomo(t, archive, subscription.YAML("localhost", bad, false), certPath)
	badConn, e := testkit.ClientConnect(badClient, echo)
	if e == nil {
		defer badConn.Close()
		if testkit.Payload(badConn) == nil {
			t.Fatal("wrong AnyTLS password accepted")
		}
	}
	wrongSNI := []byte(strings.Replace(string(yaml), `sni: "localhost"`, `sni: "wrong.example.com"`, 1))
	wrongTLSClient := testkit.Mihomo(t, archive, wrongSNI, certPath)
	wrongTLS, e := testkit.ClientConnect(wrongTLSClient, echo)
	if e == nil {
		defer wrongTLS.Close()
		if testkit.Payload(wrongTLS) == nil {
			t.Fatal("hostname-mismatched certificate accepted")
		}
	}
	if e = testkit.Payload(c); e != nil {
		t.Fatal("independent clients interrupted held stream", e)
	}
	t.Log("PASS AnyTLS real Mihomo TCP + verified TLS + authentication + server-side UDP rejection", arch)
}
