// Copyright 2023 Google LLC. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package distributor_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/golang/glog"
	"github.com/google/go-cmp/cmp"
	"github.com/google/trillian-examples/distributor/cmd/internal/distributor"
	docktest "github.com/google/trillian-examples/internal/testonly/docker"
	"github.com/ory/dockertest/v3"
	"github.com/transparency-dev/formats/log"
	"golang.org/x/mod/sumdb/note"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	_ "github.com/go-sql-driver/mysql"
)

var (
	logFoo = fakeLog{
		LogInfo: distributor.LogInfo{
			Origin:   "from foo",
			Verifier: verifierOrDie("FooLog+3d42aea6+Aby03a35YY+FNI4dfRSvLtq1jQE5UjxIW5CXfK0hiIac"),
		},
		signer: signerOrDie("PRIVATE+KEY+FooLog+3d42aea6+AdLOqvyC6Q/86GltHux+trlUT3fRKyCtnc/1VMrmLIdo"),
	}
	logBar = fakeLog{
		LogInfo: distributor.LogInfo{
			Origin:   "from bar",
			Verifier: verifierOrDie("BarLog+74e9e60a+AQXax81tHt0hpLWhLfnmZ677jAQ7+PLWenJqNrj83CeC"),
		},
		signer: signerOrDie("PRIVATE+KEY+BarLog+74e9e60a+AckT6UKhbEXLxB57ZoqJNWRFsUJ+T6hnZrDd7G+SfZ5h"),
	}
	witWhittle = fakeWitness{
		verifier: verifierOrDie("Whittle+0fc7a204+AVcy4ozqLddii0hxKZNAmBiUIv7yFolUC+fUB/O44GLI"),
		signer:   signerOrDie("PRIVATE+KEY+Whittle+0fc7a204+AfzcRAGTc9Lrim47fDQ+elRKfflP92RXAkPqAojYkcaJ"),
	}
	witWattle = fakeWitness{
		verifier: verifierOrDie("Wattle+1c75450a+AYHI4pLRIKv6LEnH+LiozE2HeMUxGXJRVHrg3Nm5UgfY"),
		signer:   signerOrDie("PRIVATE+KEY+Wattle+1c75450a+ASVbnzJKChp9hp1lUGX9ybsUDQK2WQOnLAefGzahraTg"),
	}
	witWaffle = fakeWitness{
		verifier: verifierOrDie("Waffle+2d9257ba+ATjKQkTEZBrM4IWzhhiBiEqCKkbmgm+JwElTDfiwEKpD"),
		signer:   signerOrDie("PRIVATE+KEY+Waffle+2d9257ba+AXcvT+ZS66Y1otACNcq2s6LxHfgY+j340rqpf2aF1/zH"),
	}
)

var helper dbHelper

type dbHelper struct {
	address string
	nextDB  uint32
}

func (h *dbHelper) connect() (*sql.DB, error) {
	connectString := fmt.Sprintf("root:secret@(%s)/mysql", h.address)
	return sql.Open("mysql", connectString)
}

func (h *dbHelper) create(testName string) (*sql.DB, error) {
	db, err := h.connect()
	if err != nil {
		return db, err
	}
	defer func() {
		if err := db.Close(); err != nil {
			glog.Errorf("db.Close(): %v", err)
		}
	}()
	dbName := fmt.Sprintf("%s_%d", testName, h.nextDB)
	h.nextDB++
	_, err = db.Exec(fmt.Sprintf("CREATE DATABASE %s", dbName))
	if err != nil {
		return nil, err
	}
	connectString := fmt.Sprintf("root:secret@(%s)/%s", h.address, dbName)
	return sql.Open("mysql", connectString)
}

func TestMain(m *testing.M) {
	flag.Parse()
	// uses a sensible default on windows (tcp/http) and linux/osx (socket)
	pool, err := dockertest.NewPool("")
	if err != nil {
		glog.Fatalf("Could not construct pool: %s", err)
	}

	// uses pool to try to connect to Docker
	if err := pool.Client.Ping(); err != nil {
		glog.Fatalf("Could not connect to Docker: %s", err)
	}
	// pulls an image, creates a container based on it and runs it
	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "mariadb",
			Tag:        "10.11.2",
			Env:        []string{"MYSQL_ROOT_PASSWORD=secret"},
		},
		docktest.ConfigureHost)
	if err != nil {
		glog.Fatalf("Could not start resource: %s", err)
	}
	// Tell docker to hard kill the container in 180 seconds
	if err := resource.Expire(180); err != nil {
		glog.Errorf("resource.Expire(): %v", err)
	}

	helper = dbHelper{
		address: docktest.GetAddress(resource),
	}
	// exponential backoff-retry, because the application in the container might not be ready to accept connections yet
	if err := retry(func() error {
		var err error
		db, err := helper.connect()
		if err != nil {
			return err
		}
		return db.Ping()
	}); err != nil {
		glog.Fatalf("Could not connect to database: %s", err)
	}

	code := m.Run()

	// You can't defer this because os.Exit doesn't care for defer
	if err := pool.Purge(resource); err != nil {
		glog.Fatalf("Could not purge resource: %s", err)
	}

	os.Exit(code)
}

func retry(op func() error) error {
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = time.Second * 5
	bo.MaxElapsedTime = time.Minute
	if err := backoff.Retry(op, bo); err != nil {
		if bo.NextBackOff() == backoff.Stop {
			return fmt.Errorf("reached retry deadline (last error: %v)", err)
		}

		return err
	}
	return nil
}

func TestGetLogs(t *testing.T) {
	ws := map[string]note.Verifier{}
	testCases := []struct {
		desc string
		logs map[string]distributor.LogInfo
		want []string
	}{
		{
			desc: "No logs",
			logs: map[string]distributor.LogInfo{},
			want: []string{},
		},
		{
			desc: "One log",
			logs: map[string]distributor.LogInfo{
				"FooLog": logFoo.LogInfo,
			},
			want: []string{"FooLog"},
		},
		{
			desc: "Two logs",
			logs: map[string]distributor.LogInfo{
				"FooLog": logFoo.LogInfo,
				"BarLog": logBar.LogInfo,
			},
			want: []string{"BarLog", "FooLog"},
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db, err := helper.create("TestGetLogs")
			if err != nil {
				t.Fatalf("helper.create(): %v", err)
			}
			d, err := distributor.NewDistributor(ws, tC.logs, db)
			if err != nil {
				t.Fatalf("NewDistributor(): %v", err)
			}
			got, err := d.GetLogs(ctx)
			if err != nil {
				t.Errorf("GetLogs(): %v", err)
			}
			if !cmp.Equal(got, tC.want) {
				t.Errorf("got %q, want %q", got, tC.want)
			}
		})
	}
}

func TestDistributeLogAndWitnessMustMatchCheckpoint(t *testing.T) {
	ws := map[string]note.Verifier{
		"Whittle": witWhittle.verifier,
		"Wattle":  witWattle.verifier,
	}
	ls := map[string]distributor.LogInfo{
		"FooLog": logFoo.LogInfo,
		"BarLog": logBar.LogInfo,
	}
	testCases := []struct {
		desc     string
		reqLogID string
		reqWitID string
		log      fakeLog
		wit      fakeWitness
		wantErr  bool
	}{
		{
			desc:     "Correct log and witness: foo and whittle",
			reqLogID: "FooLog",
			reqWitID: "Whittle",
			log:      logFoo,
			wit:      witWhittle,
			wantErr:  false,
		},
		{
			desc:     "Correct log and witness: bar and wattle",
			reqLogID: "BarLog",
			reqWitID: "Wattle",
			log:      logBar,
			wit:      witWattle,
			wantErr:  false,
		},
		{
			desc:     "Correct log wrong witness",
			reqLogID: "FooLog",
			reqWitID: "Whittle",
			log:      logFoo,
			wit:      witWattle,
			wantErr:  true,
		},
		{
			desc:     "Wrong log correct witness",
			reqLogID: "BarLog",
			reqWitID: "Whittle",
			log:      logFoo,
			wit:      witWhittle,
			wantErr:  true,
		},
		{
			desc:     "Wrong log wrong witness",
			reqLogID: "BarLog",
			reqWitID: "Whittle",
			log:      logFoo,
			wit:      witWattle,
			wantErr:  true,
		},
		{
			desc:     "Unknown log known witness",
			reqLogID: "DogNotLog",
			reqWitID: "Wattle",
			log:      logFoo,
			wit:      witWattle,
			wantErr:  true,
		},
		{
			desc:     "Correct log unknown witness",
			reqLogID: "FooLog",
			reqWitID: "WhatAWally",
			log:      logFoo,
			wit:      witWattle,
			wantErr:  true,
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db, err := helper.create("TestDistributeLogAndWitnessMustMatchCheckpoint")
			if err != nil {
				t.Fatalf("helper.create(): %v", err)
			}
			d, err := distributor.NewDistributor(ws, ls, db)
			if err != nil {
				t.Fatalf("NewDistributor(): %v", err)
			}

			logCP16 := tC.log.checkpoint(16, "16", tC.wit.signer)
			err = d.Distribute(ctx, tC.reqLogID, tC.reqWitID, logCP16)
			if (err != nil) != tC.wantErr {
				t.Errorf("unexpected error output (wantErr: %t): %v", tC.wantErr, err)
			}
		})
	}
}

func TestDistributeEvolution(t *testing.T) {
	// The base case for this test is that a single checkpoint has already
	// been registered for log foo, by whittle, at tree size 16, with root hash H("16").
	ws := map[string]note.Verifier{
		"Whittle": witWhittle.verifier,
		"Wattle":  witWattle.verifier,
	}
	ls := map[string]distributor.LogInfo{
		"FooLog": logFoo.LogInfo,
		"BarLog": logBar.LogInfo,
	}
	testCases := []struct {
		desc     string
		log      fakeLog
		wit      fakeWitness
		size     uint64
		hashSeed string
		wantErr  bool
	}{
		{
			desc:     "whittle a bit bigger",
			log:      logFoo,
			wit:      witWhittle,
			size:     18,
			hashSeed: "18",
			wantErr:  false,
		},
		{
			desc:     "whittle smaller",
			log:      logFoo,
			wit:      witWhittle,
			size:     11,
			hashSeed: "11",
			wantErr:  true,
		},
		{
			desc:     "whittle same",
			log:      logFoo,
			wit:      witWhittle,
			size:     16,
			hashSeed: "16",
			wantErr:  false,
		},
		{
			desc:     "whittle same size but different hash",
			log:      logFoo,
			wit:      witWhittle,
			size:     16,
			hashSeed: "not 16",
			wantErr:  true,
		},
		{
			desc:     "whittle smaller different log",
			log:      logBar,
			wit:      witWhittle,
			size:     11,
			hashSeed: "11",
			wantErr:  false,
		},
		{
			desc:     "wattle smaller",
			log:      logFoo,
			wit:      witWattle,
			size:     11,
			hashSeed: "11",
			wantErr:  false,
		},
		{
			desc:     "wattle same size",
			log:      logFoo,
			wit:      witWattle,
			size:     16,
			hashSeed: "16",
			wantErr:  false,
		},
		{
			desc:     "wattle same size but different hash",
			log:      logFoo,
			wit:      witWattle,
			size:     16,
			hashSeed: "not 16",
			wantErr:  false, // We don't check consistency with all witnesses on write
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db, err := helper.create("TestDistributeEvolution")
			if err != nil {
				t.Fatalf("helper.create(): %v", err)
			}
			d, err := distributor.NewDistributor(ws, ls, db)
			if err != nil {
				t.Fatalf("NewDistributor(): %v", err)
			}
			err = d.Distribute(ctx, "FooLog", "Whittle", logFoo.checkpoint(16, "16", witWhittle.signer))
			if err != nil {
				t.Fatalf("Distribute(): %v", err)
			}

			err = d.Distribute(ctx, tC.log.Verifier.Name(), tC.wit.verifier.Name(), tC.log.checkpoint(tC.size, tC.hashSeed, tC.wit.signer))
			if (err != nil) != tC.wantErr {
				t.Errorf("unexpected error output (wantErr: %t): %v", tC.wantErr, err)
			}
		})
	}
}

func TestGetCheckpointWitness(t *testing.T) {
	// The base case for this test is that a single checkpoint has already
	// been registered for log foo, by whittle, at tree size 16, with root hash H("16").
	ws := map[string]note.Verifier{
		"Whittle": witWhittle.verifier,
		"Wattle":  witWattle.verifier,
	}
	ls := map[string]distributor.LogInfo{
		"FooLog": logFoo.LogInfo,
		"BarLog": logBar.LogInfo,
	}
	testCases := []struct {
		desc    string
		log     fakeLog
		wit     fakeWitness
		wantErr bool
	}{
		{
			desc:    "read back same cp",
			log:     logFoo,
			wit:     witWhittle,
			wantErr: false,
		},
		{
			desc:    "same log, different witness",
			log:     logFoo,
			wit:     witWattle,
			wantErr: true,
		},
		{
			desc:    "different log, same witness",
			log:     logBar,
			wit:     witWhittle,
			wantErr: true,
		},
		{
			desc:    "different log, different witness",
			log:     logBar,
			wit:     witWattle,
			wantErr: true,
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db, err := helper.create("TestGetCheckpointWitness")
			if err != nil {
				t.Fatalf("helper.create(): %v", err)
			}
			d, err := distributor.NewDistributor(ws, ls, db)
			if err != nil {
				t.Fatalf("NewDistributor(): %v", err)
			}
			writeCP := logFoo.checkpoint(16, "16", witWhittle.signer)
			err = d.Distribute(ctx, "FooLog", "Whittle", writeCP)
			if err != nil {
				t.Fatalf("Distribute(): %v", err)
			}

			readCP, err := d.GetCheckpointWitness(ctx, tC.log.Verifier.Name(), tC.wit.verifier.Name())
			if (err != nil) != tC.wantErr {
				t.Errorf("unexpected error output (wantErr: %t): %v", tC.wantErr, err)
			}
			if !tC.wantErr {
				if !cmp.Equal(readCP, writeCP) {
					t.Errorf("Written checkpoint != read checkpoint. Read\n%v\n\nWrote:\n%v", readCP, writeCP)
				}
			}
		})
	}
}

func TestGetCheckpointN(t *testing.T) {
	// The base case for this test is that 2 checkpoints have already been written:
	//  - whittle, at tree size 16
	//  - waffle, at tree size 14
	ws := map[string]note.Verifier{
		"Whittle": witWhittle.verifier,
		"Wattle":  witWattle.verifier,
		"Waffle":  witWaffle.verifier,
	}
	ls := map[string]distributor.LogInfo{
		"FooLog": logFoo.LogInfo,
		"BarLog": logBar.LogInfo,
	}
	testCases := []struct {
		desc        string
		distWit     fakeWitness
		distLog     fakeLog
		distSize    uint64
		reqLog      string
		reqN        uint32
		wantErr     bool
		wantErrCode codes.Code
		wantSize    uint64
		wantWits    []note.Verifier
	}{
		{
			desc:        "unknown log is error",
			distWit:     witWattle,
			distLog:     logFoo,
			distSize:    10,
			reqLog:      "ThisIsNotTheLogYouAreLookingFor",
			reqN:        1,
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
		{
			desc:     "smaller checkpoint doesn't win",
			distWit:  witWattle,
			distLog:  logFoo,
			distSize: 10,
			reqLog:   "FooLog",
			reqN:     1,
			wantErr:  false,
			wantSize: 16,
			wantWits: []note.Verifier{witWhittle.verifier},
		},
		{
			desc:     "larger checkpoint wins",
			distWit:  witWattle,
			distLog:  logFoo,
			distSize: 20,
			reqLog:   "FooLog",
			reqN:     1,
			wantErr:  false,
			wantSize: 20,
			wantWits: []note.Verifier{witWattle.verifier},
		},
		{
			desc:     "same size checkpoint merges",
			distWit:  witWattle,
			distLog:  logFoo,
			distSize: 16,
			reqLog:   "FooLog",
			reqN:     2,
			wantErr:  false,
			wantSize: 16,
			wantWits: []note.Verifier{witWattle.verifier, witWhittle.verifier},
		},
		{
			desc:     "merge with smaller checkpoint",
			distWit:  witWattle,
			distLog:  logFoo,
			distSize: 14,
			reqLog:   "FooLog",
			reqN:     2,
			wantErr:  false,
			wantSize: 14,
			wantWits: []note.Verifier{witWattle.verifier, witWaffle.verifier},
		},
		{
			desc:     "more sigs can be returned than needed",
			distWit:  witWattle,
			distLog:  logFoo,
			distSize: 16,
			reqLog:   "FooLog",
			reqN:     1,
			wantErr:  false,
			wantSize: 16,
			wantWits: []note.Verifier{witWattle.verifier, witWhittle.verifier},
		},
		{
			desc:        "error returned if not enough sigs",
			distWit:     witWattle,
			distLog:     logFoo,
			distSize:    16,
			reqLog:      "FooLog",
			reqN:        3,
			wantErr:     true,
			wantErrCode: codes.NotFound,
		},
		{
			desc:        "zero invalid",
			distWit:     witWattle,
			distLog:     logFoo,
			distSize:    16,
			reqLog:      "FooLog",
			reqN:        0,
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
		{
			desc:        "huge number invalid",
			distWit:     witWattle,
			distLog:     logFoo,
			distSize:    16,
			reqLog:      "FooLog",
			reqN:        999,
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db, err := helper.create("TestGetCheckpointN")
			if err != nil {
				t.Fatalf("helper.create(): %v", err)
			}
			d, err := distributor.NewDistributor(ws, ls, db)
			if err != nil {
				t.Fatalf("NewDistributor(): %v", err)
			}
			if err := d.Distribute(ctx, "FooLog", "Whittle", logFoo.checkpoint(16, "16", witWhittle.signer)); err != nil {
				t.Fatal(err)
			}
			if err := d.Distribute(ctx, "FooLog", "Waffle", logFoo.checkpoint(14, "14", witWaffle.signer)); err != nil {
				t.Fatal(err)
			}

			if err := d.Distribute(ctx, tC.distLog.Verifier.Name(), tC.distWit.verifier.Name(), tC.distLog.checkpoint(tC.distSize, fmt.Sprintf("%d", tC.distSize), tC.distWit.signer)); err != nil {
				t.Fatal(err)
			}

			cpRaw, err := d.GetCheckpointN(ctx, tC.reqLog, tC.reqN)
			if (err != nil) != tC.wantErr {
				t.Fatalf("unexpected error output (wantErr: %t): %v", tC.wantErr, err)
			}
			if !tC.wantErr {
				cp, _, n, err := log.ParseCheckpoint(cpRaw, tC.distLog.Origin, tC.distLog.Verifier, tC.wantWits...)
				if err != nil {
					t.Error(err)
				}
				if got, want := len(n.Sigs), 1+len(tC.wantWits); got != want {
					t.Errorf("expected %d sigs, got %d", want, got)
				}
				if cp.Size != tC.wantSize {
					t.Errorf("expected tree size of %d but got %d", tC.wantSize, cp.Size)
				}
			} else {
				if got, want := status.Code(err), tC.wantErrCode; got != want {
					t.Errorf("error code got != want: %v != %v", got, want)
				}
			}
		})
	}
}

func verifierOrDie(vkey string) note.Verifier {
	v, err := note.NewVerifier(vkey)
	if err != nil {
		panic(err)
	}
	return v
}

func signerOrDie(skey string) note.Signer {
	s, err := note.NewSigner(skey)
	if err != nil {
		panic(err)
	}
	return s
}

type fakeLog struct {
	distributor.LogInfo
	signer note.Signer
}

func (l fakeLog) checkpoint(size uint64, hashSeed string, wit note.Signer) []byte {
	hbs := sha256.Sum256([]byte(hashSeed))
	rawCP := log.Checkpoint{
		Origin: l.Origin,
		Size:   size,
		Hash:   hbs[:],
	}.Marshal()
	n := note.Note{}
	n.Text = string(rawCP)
	bs, err := note.Sign(&n, []note.Signer{l.signer, wit}...)
	if err != nil {
		panic(err)
	}
	return bs
}

type fakeWitness struct {
	verifier note.Verifier
	signer   note.Signer
}
