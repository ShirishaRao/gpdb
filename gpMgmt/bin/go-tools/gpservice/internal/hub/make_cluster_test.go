package hub_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"os/user"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/golang/mock/gomock"

	"github.com/greenplum-db/gp-common-go-libs/testhelper"
	"github.com/greenplum-db/gpdb/gpservice/idl"
	"github.com/greenplum-db/gpdb/gpservice/idl/mock_idl"
	"github.com/greenplum-db/gpdb/gpservice/internal/hub"
	"github.com/greenplum-db/gpdb/gpservice/internal/testutils"
	"github.com/greenplum-db/gpdb/gpservice/internal/testutils/exectest"
	"github.com/greenplum-db/gpdb/gpservice/pkg/greenplum"
	"github.com/greenplum-db/gpdb/gpservice/pkg/utils"
)

func init() {
	exectest.RegisterMains()
}

func TestCreateSegments(t *testing.T) {
	testhelper.SetupTestLogger()
	hubServer := hub.New(testutils.CreateDummyServiceConfig(t))

	segs := []greenplum.Segment{
		{
			Port:     1111,
			DataDir:  "/gpseg0",
			Address:  "sdw1",
			Hostname: "sdw1",
		},
		{
			Port:     2222,
			DataDir:  "/gpseg1",
			Address:  "sdw2",
			Hostname: "sdw2",
		},
		{
			Port:     3333,
			DataDir:  "/gpseg2",
			Address:  "sdw2",
			Hostname: "sdw2",
		},
	}

	commonConfig := map[string]string{
		"key1": "value1",
	}
	coordinatorConfig := map[string]string{
		"key2": "value2",
	}
	segConfig := map[string]string{
		"key3": "value3",
	}

	t.Run("successfully creates the segments", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		expectedSegConfig := make(map[string]string)
		maps.Copy(expectedSegConfig, commonConfig)
		maps.Copy(expectedSegConfig, segConfig)

		sdw1 := mock_idl.NewMockAgentClient(ctrl)
		sdw1.EXPECT().MakeSegment(
			gomock.Any(),
			&idl.MakeSegmentRequest{
				Segment:          segmentToProto(segs[0]),
				SegConfig:        expectedSegConfig,
				CoordinatorAddrs: make([]string, 0),
			},
		).Return(&idl.MakeSegmentReply{}, nil)

		sdw2 := mock_idl.NewMockAgentClient(ctrl)
		sdw2.EXPECT().MakeSegment(
			gomock.Any(),
			&idl.MakeSegmentRequest{
				Segment:          segmentToProto(segs[1]),
				SegConfig:        expectedSegConfig,
				CoordinatorAddrs: make([]string, 0),
			},
		).Return(&idl.MakeSegmentReply{}, nil)

		sdw2.EXPECT().MakeSegment(
			gomock.Any(),
			&idl.MakeSegmentRequest{
				Segment:          segmentToProto(segs[2]),
				SegConfig:        expectedSegConfig,
				CoordinatorAddrs: make([]string, 0),
			},
		).Return(&idl.MakeSegmentReply{}, nil)

		agentConns := []*hub.Connection{
			{AgentClient: sdw1, Hostname: "sdw1"},
			{AgentClient: sdw2, Hostname: "sdw2"},
		}
		hubServer.Conns = agentConns

		clusterParams := &idl.ClusterParams{
			CommonConfig:      commonConfig,
			CoordinatorConfig: coordinatorConfig,
			SegmentConfig:     segConfig,
		}

		mock, stream := testutils.NewMockStream()
		err := hubServer.CreateSegments(context.Background(), mock, segs, clusterParams, []string{})
		if err != nil {
			t.Fatalf("unexpected error: %#v", err)
		}

		expectedStreamResponse := make([]*idl.HubReply, len(segs)+1)
		for i := range expectedStreamResponse {
			expectedStreamResponse[i] = &idl.HubReply{
				Message: &idl.HubReply_ProgressMsg{
					ProgressMsg: &idl.ProgressMessage{
						Label:   "Initializing primary segments:",
						Current: int32(i),
						Total:   3,
					},
				},
			}
		}

		if !reflect.DeepEqual(stream.GetBuffer(), expectedStreamResponse) {
			t.Fatalf("got %+v, want %+v", stream.GetBuffer(), expectedStreamResponse)
		}
	})

	t.Run("when fails to create one of the segments", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		expectedErr := errors.New("error")

		sdw1 := mock_idl.NewMockAgentClient(ctrl)
		sdw1.EXPECT().MakeSegment(
			gomock.Any(),
			gomock.Any(),
		).Return(&idl.MakeSegmentReply{}, nil)

		sdw2 := mock_idl.NewMockAgentClient(ctrl)
		sdw2.EXPECT().MakeSegment(
			gomock.Any(),
			gomock.Any(),
		).Return(&idl.MakeSegmentReply{}, expectedErr)

		sdw2.EXPECT().MakeSegment(
			gomock.Any(),
			gomock.Any(),
		).Return(&idl.MakeSegmentReply{}, nil)

		agentConns := []*hub.Connection{
			{AgentClient: sdw1, Hostname: "sdw1"},
			{AgentClient: sdw2, Hostname: "sdw2"},
		}
		hubServer.Conns = agentConns

		clusterParams := &idl.ClusterParams{
			CommonConfig:      commonConfig,
			CoordinatorConfig: coordinatorConfig,
			SegmentConfig:     segConfig,
		}

		mock, stream := testutils.NewMockStream()
		err := hubServer.CreateSegments(context.Background(), mock, segs, clusterParams, []string{})
		if !errors.Is(err, expectedErr) {
			t.Fatalf("got %#v, want %#V", err, expectedErr)
		}

		expectedStreamResponse := make([]*idl.HubReply, 3)
		for i := range expectedStreamResponse {
			expectedStreamResponse[i] = &idl.HubReply{
				Message: &idl.HubReply_ProgressMsg{
					ProgressMsg: &idl.ProgressMessage{
						Label:   "Initializing primary segments:",
						Current: int32(i),
						Total:   3,
					},
				},
			}
		}

		if !reflect.DeepEqual(stream.GetBuffer(), expectedStreamResponse) {
			t.Fatalf("got %+v, want %+v", stream.GetBuffer(), expectedStreamResponse)
		}
	})

	t.Run("successfully creates and starts the coordinator segment", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		expectedSegConfig := make(map[string]string)
		maps.Copy(expectedSegConfig, commonConfig)
		maps.Copy(expectedSegConfig, coordinatorConfig)

		seg := &idl.Segment{
			Port:          1111,
			DataDirectory: "/gpseg-1",
			HostName:      "cdw",
			HostAddress:   "cdw",
			Contentid:     -1,
			Dbid:          1,
		}

		cdw := mock_idl.NewMockAgentClient(ctrl)
		cdw.EXPECT().MakeSegment(
			gomock.Any(),
			&idl.MakeSegmentRequest{
				Segment:          seg,
				SegConfig:        expectedSegConfig,
				CoordinatorAddrs: make([]string, 0),
			},
		).Return(&idl.MakeSegmentReply{}, nil)

		cdw.EXPECT().StartSegment(
			gomock.Any(),
			&idl.StartSegmentRequest{
				DataDir: seg.DataDirectory,
				Wait:    true,
				Options: "-c gp_role=utility",
			},
		).Return(&idl.StartSegmentReply{}, nil)

		agentConns := []*hub.Connection{
			{AgentClient: cdw, Hostname: "cdw"},
		}
		hubServer.Conns = agentConns

		clusterParams := &idl.ClusterParams{
			CommonConfig:      commonConfig,
			CoordinatorConfig: coordinatorConfig,
			SegmentConfig:     segConfig,
		}

		err := hubServer.CreateAndStartCoordinator(context.Background(), seg, clusterParams)
		if err != nil {
			t.Fatalf("unexpected error: %#v", err)
		}
	})

	t.Run("when fails to create the coordinator segment", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		expectedSegConfig := make(map[string]string)
		maps.Copy(expectedSegConfig, commonConfig)
		maps.Copy(expectedSegConfig, coordinatorConfig)

		seg := &idl.Segment{
			Port:          1111,
			DataDirectory: "/gpseg-1",
			HostName:      "cdw",
			HostAddress:   "cdw",
			Contentid:     -1,
			Dbid:          1,
		}

		expectedErr := errors.New("error")
		cdw := mock_idl.NewMockAgentClient(ctrl)
		cdw.EXPECT().MakeSegment(
			gomock.Any(),
			&idl.MakeSegmentRequest{
				Segment:          seg,
				SegConfig:        expectedSegConfig,
				CoordinatorAddrs: make([]string, 0),
			},
		).Return(&idl.MakeSegmentReply{}, expectedErr)

		agentConns := []*hub.Connection{
			{AgentClient: cdw, Hostname: "cdw"},
		}
		hubServer.Conns = agentConns

		clusterParams := &idl.ClusterParams{
			CommonConfig:      commonConfig,
			CoordinatorConfig: coordinatorConfig,
			SegmentConfig:     segConfig,
		}

		err := hubServer.CreateAndStartCoordinator(context.Background(), seg, clusterParams)
		if !errors.Is(err, expectedErr) {
			t.Fatalf("got %#v, want %#v", err, expectedErr)
		}
	})
}

func TestStopCoordinator(t *testing.T) {
	testhelper.SetupTestLogger()
	hubServer := hub.New(testutils.CreateDummyServiceConfig(t))

	t.Run("successfully stops the coordinator segment", func(t *testing.T) {
		var pgCtlCalled bool

		utils.System.ExecCommand = exectest.NewCommandWithVerifier(exectest.Success, func(utility string, args ...string) {
			pgCtlCalled = true

			expectedUtility := "gpHome/bin/pg_ctl"
			if utility != expectedUtility {
				t.Fatalf("got %s, want %s", utility, expectedUtility)
			}

			expectedArgs := []string{"stop", "--pgdata", "gpseg-1"}
			if !reflect.DeepEqual(args, expectedArgs) {
				t.Fatalf("got %+v, want %+v", args, expectedArgs)
			}
		})
		defer utils.ResetSystemFunctions()

		mock, stream := testutils.NewMockStream()
		err := hubServer.StopCoordinator(mock, "gpseg-1")
		if err != nil {
			t.Fatalf("unexpected error: %#v", err)
		}

		if !pgCtlCalled {
			t.Fatalf("expected pg_ctl to be called")
		}

		expectedStreamResponse := []*idl.HubReply{
			{
				Message: &idl.HubReply_LogMsg{
					LogMsg: &idl.LogMessage{
						Message: "Shutting down coordinator segment",
						Level:   idl.LogLevel_INFO,
					},
				},
			},
			{
				Message: &idl.HubReply_LogMsg{
					LogMsg: &idl.LogMessage{
						Message: "Successfully shut down coordinator segment",
						Level:   idl.LogLevel_INFO,
					},
				},
			},
		}

		if !reflect.DeepEqual(stream.GetBuffer(), expectedStreamResponse) {
			t.Fatalf("got %+v, want %+v", stream.GetBuffer(), expectedStreamResponse)
		}
	})

	t.Run("fails to stop the coordinator segment", func(t *testing.T) {
		utils.System.ExecCommand = exectest.NewCommand(exectest.Failure)
		defer utils.ResetSystemFunctions()

		mock, stream := testutils.NewMockStream()
		expectedErrPrefix := "executing pg_ctl stop:"

		err := hubServer.StopCoordinator(mock, "gpseg-1")
		if !strings.HasPrefix(err.Error(), expectedErrPrefix) {
			t.Fatalf("got %v, want prefix %v", err, expectedErrPrefix)
		}

		var expectedErr *exec.ExitError
		if !errors.As(err, &expectedErr) {
			t.Errorf("got %T, want %T", err, expectedErr)
		}

		expectedStreamResponse := []*idl.HubReply{
			{
				Message: &idl.HubReply_LogMsg{
					LogMsg: &idl.LogMessage{
						Message: "Shutting down coordinator segment",
						Level:   idl.LogLevel_INFO,
					},
				},
			},
		}

		if !reflect.DeepEqual(stream.GetBuffer(), expectedStreamResponse) {
			t.Fatalf("got %+v, want %+v", stream.GetBuffer(), expectedStreamResponse)
		}
	})
}

func TestValidateEnvironment(t *testing.T) {
	testhelper.SetupTestLogger()
	hubServer := hub.New(testutils.CreateDummyServiceConfig(t))

	segs := []greenplum.Segment{
		{
			Port:     1111,
			DataDir:  "/gpseg-1",
			Address:  "cdw",
			Hostname: "cdw",
		},
		{
			Port:     2222,
			DataDir:  "/gpseg0",
			Address:  "sdw1",
			Hostname: "sdw1",
		},
		{
			Port:     3333,
			DataDir:  "/gpseg1",
			Address:  "sdw2-1",
			Hostname: "sdw2",
		},
		{
			Port:     4444,
			DataDir:  "/gpseg2",
			Address:  "sdw2-2",
			Hostname: "sdw2",
		},
	}

	req := &idl.MakeClusterRequest{
		GpArray: &idl.GpArray{
			Coordinator: segmentToProto(segs[0]),
			SegmentArray: []*idl.SegmentPair{
				{
					Primary: segmentToProto(segs[1]),
				},
				{
					Primary: segmentToProto(segs[2]),
				},
				{
					Primary: segmentToProto(segs[3]),
				},
			},
		},
		ClusterParams: &idl.ClusterParams{
			Locale: &idl.Locale{},
		},
	}

	t.Run("successfully validates the segment hosts", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cdw := mock_idl.NewMockAgentClient(ctrl)
		cdw.EXPECT().ValidateHostEnv(
			gomock.Any(),
			&idl.ValidateHostEnvRequest{
				HostAddressList: []string{segs[0].Address},
				DirectoryList:   []string{segs[0].DataDir},
				Locale:          &idl.Locale{},
				PortList:        []string{fmt.Sprintf("%d", segs[0].Port)},
				Forced:          false,
			},
		).Return(&idl.ValidateHostEnvReply{}, nil)

		sdw1 := mock_idl.NewMockAgentClient(ctrl)
		sdw1.EXPECT().ValidateHostEnv(
			gomock.Any(),
			&idl.ValidateHostEnvRequest{
				HostAddressList: []string{segs[1].Address},
				DirectoryList:   []string{segs[1].DataDir},
				Locale:          &idl.Locale{},
				PortList:        []string{fmt.Sprintf("%d", segs[1].Port)},
				Forced:          false,
			},
		).Return(&idl.ValidateHostEnvReply{
			Messages: []*idl.LogMessage{
				{
					Message: "message",
					Level:   idl.LogLevel_WARNING,
				},
			},
		}, nil)

		sdw2 := mock_idl.NewMockAgentClient(ctrl)
		sdw2.EXPECT().ValidateHostEnv(
			gomock.Any(),
			&idl.ValidateHostEnvRequest{
				HostAddressList: []string{segs[2].Address, segs[3].Address},
				DirectoryList:   []string{segs[2].DataDir, segs[3].DataDir},
				Locale:          &idl.Locale{},
				PortList:        []string{strconv.Itoa(segs[2].Port), strconv.Itoa(segs[3].Port)},
				Forced:          false,
			},
		).Return(&idl.ValidateHostEnvReply{}, nil)

		agentConns := []*hub.Connection{
			{AgentClient: cdw, Hostname: "cdw"},
			{AgentClient: sdw1, Hostname: "sdw1"},
			{AgentClient: sdw2, Hostname: "sdw2"},
		}
		hubServer.Conns = agentConns

		var postgresCalled bool
		utils.System.ExecCommand = exectest.NewCommandWithVerifier(exectest.Success, func(utility string, args ...string) {
			postgresCalled = true

			expectedUtility := "gpHome/bin/postgres"
			if utility != expectedUtility {
				t.Fatalf("got %v, want %v", utility, expectedUtility)
			}

			expectedArgs := []string{"--gp-version"}
			if !reflect.DeepEqual(args, expectedArgs) {
				t.Fatalf("got %+v, want %+v", args, expectedArgs)
			}
		})
		defer utils.ResetSystemFunctions()

		mock, stream := testutils.NewMockStream()
		err := hubServer.ValidateEnvironment(context.Background(), mock, req)
		if err != nil {
			t.Fatalf("unexpected error: %#v", err)
		}

		if !postgresCalled {
			t.Fatalf("expected postgres to be called")
		}

		expectedStreamResponse := make([]*idl.HubReply, len(hubServer.Conns)+1)
		for i := range expectedStreamResponse {
			expectedStreamResponse[i] = &idl.HubReply{
				Message: &idl.HubReply_ProgressMsg{
					ProgressMsg: &idl.ProgressMessage{
						Label:   "Validating Hosts:",
						Current: int32(i),
						Total:   3,
					},
				},
			}
		}

		expectedStreamResponse = append(expectedStreamResponse, &idl.HubReply{
			Message: &idl.HubReply_LogMsg{
				LogMsg: &idl.LogMessage{
					Message: "Host: sdw1 message",
					Level:   idl.LogLevel_WARNING,
				},
			},
		})

		if !reflect.DeepEqual(stream.GetBuffer(), expectedStreamResponse) {
			t.Fatalf("got %+v, want %+v", stream.GetBuffer(), expectedStreamResponse)
		}
	})

	t.Run("errors out when not able to get the postgres version", func(t *testing.T) {
		utils.System.ExecCommand = exectest.NewCommand(exectest.Failure)
		defer utils.ResetSystemFunctions()

		mock, stream := testutils.NewMockStream()
		err := hubServer.ValidateEnvironment(context.Background(), mock, req)

		expectedErrPrefix := "fetching postgres gp-version:"
		if !strings.HasPrefix(err.Error(), expectedErrPrefix) {
			t.Fatalf("got %v, want prefix %v", err, expectedErrPrefix)
		}

		var expectedErr *exec.ExitError
		if !errors.As(err, &expectedErr) {
			t.Fatalf("got %T, want %T", err, expectedErr)
		}

		if len(stream.GetBuffer()) != 0 {
			t.Fatalf("expected no stream responses, got %+v", stream.GetBuffer())
		}
	})

	t.Run("errors out when it fails to validate on one of the hosts", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		expectedErr := errors.New("error")

		cdw := mock_idl.NewMockAgentClient(ctrl)
		cdw.EXPECT().ValidateHostEnv(
			gomock.Any(),
			gomock.Any(),
		).Return(&idl.ValidateHostEnvReply{}, nil)

		sdw1 := mock_idl.NewMockAgentClient(ctrl)
		sdw1.EXPECT().ValidateHostEnv(
			gomock.Any(),
			gomock.Any(),
		).Return(&idl.ValidateHostEnvReply{}, expectedErr)

		sdw2 := mock_idl.NewMockAgentClient(ctrl)
		sdw2.EXPECT().ValidateHostEnv(
			gomock.Any(),
			gomock.Any(),
		).Return(&idl.ValidateHostEnvReply{}, nil)

		agentConns := []*hub.Connection{
			{AgentClient: cdw, Hostname: "cdw"},
			{AgentClient: sdw1, Hostname: "sdw1"},
			{AgentClient: sdw2, Hostname: "sdw2"},
		}
		hubServer.Conns = agentConns

		utils.System.ExecCommand = exectest.NewCommand(exectest.Success)
		defer utils.ResetSystemFunctions()

		mock, stream := testutils.NewMockStream()
		err := hubServer.ValidateEnvironment(context.Background(), mock, req)

		expectedErrPrefix := "host: sdw1"
		if !strings.HasPrefix(err.Error(), expectedErrPrefix) {
			t.Fatalf("got %v, want prefix %v", err, expectedErrPrefix)
		}

		if !errors.Is(err, expectedErr) {
			t.Fatalf("got %#v, want %#v", err, expectedErr)
		}

		expectedStreamResponse := make([]*idl.HubReply, 3)
		for i := range expectedStreamResponse {
			expectedStreamResponse[i] = &idl.HubReply{
				Message: &idl.HubReply_ProgressMsg{
					ProgressMsg: &idl.ProgressMessage{
						Label:   "Validating Hosts:",
						Current: int32(i),
						Total:   3,
					},
				},
			}
		}

		if !reflect.DeepEqual(stream.GetBuffer(), expectedStreamResponse) {
			t.Fatalf("got %+v, want %+v", stream.GetBuffer(), expectedStreamResponse)
		}
	})
}

func TestCreateGpToolkitExt(t *testing.T) {
	testhelper.SetupTestLogger()

	t.Run("succesfully creates the database extensions", func(t *testing.T) {
		utils.SetExecOnDatabase(func(conn *utils.DBConnWithContext, dbname, query string) error {
			return nil
		})
		defer utils.ResetExecOnDatabase()

		conn := &utils.DBConnWithContext{}
		err := hub.CreateGpToolkitExt(conn)
		if err != nil {
			t.Fatalf("unexpected error: %#v", err)
		}
	})

	t.Run("fails to create the database extensions", func(t *testing.T) {
		expectedErr := errors.New("error")
		utils.SetExecOnDatabase(func(conn *utils.DBConnWithContext, dbname, query string) error {
			return expectedErr
		})
		defer utils.ResetExecOnDatabase()

		conn := &utils.DBConnWithContext{}
		err := hub.CreateGpToolkitExt(conn)
		if !errors.Is(err, expectedErr) {
			t.Fatalf("got %#v, want %#v", err, expectedErr)
		}
	})
}

func TestImportCollation(t *testing.T) {
	testhelper.SetupTestLogger()

	t.Run("succesfully imports collations", func(t *testing.T) {
		utils.SetExecOnDatabase(func(conn *utils.DBConnWithContext, dbname, query string) error {
			return nil
		})
		defer utils.ResetExecOnDatabase()

		conn := &utils.DBConnWithContext{}
		err := hub.ImportCollation(conn)
		if err != nil {
			t.Fatalf("unexpected error: %#v", err)
		}
	})

	cases := []struct {
		dbname string
		query  string
	}{
		{
			dbname: "postgres",
			query:  "ALTER DATABASE template0 ALLOW_CONNECTIONS on",
		},
		{
			dbname: "template0",
			query:  "SELECT pg_import_system_collations('pg_catalog'); ANALYZE;",
		},
		{
			dbname: "template0",
			query:  "VACUUM FREEZE",
		},
		{
			dbname: "postgres",
			query:  "ALTER DATABASE template0 ALLOW_CONNECTIONS off",
		},
		{
			dbname: "template1",
			query:  "SELECT pg_import_system_collations('pg_catalog'); ANALYZE;",
		},
		{
			dbname: "template1",
			query:  "VACUUM FREEZE",
		},
	}

	for _, tc := range cases {
		t.Run("fails to import collations", func(t *testing.T) {
			expectedErr := errors.New("error")
			utils.SetExecOnDatabase(func(conn *utils.DBConnWithContext, dbname, query string) error {
				if tc.dbname == dbname && tc.query == query {
					return expectedErr
				}

				return nil
			})
			defer utils.ResetExecOnDatabase()

			conn := &utils.DBConnWithContext{}
			err := hub.ImportCollation(conn)
			if !errors.Is(err, expectedErr) {
				t.Fatalf("got %#v, want %#v", err, expectedErr)
			}
		})
	}
}

func TestCreateDatabase(t *testing.T) {
	testhelper.SetupTestLogger()

	t.Run("succesfully creates the database", func(t *testing.T) {
		utils.SetExecOnDatabase(func(conn *utils.DBConnWithContext, dbname, query string) error {
			expectedQuery := `CREATE DATABASE "testdb"`
			if query != expectedQuery {
				t.Fatalf("got %v, want %v", query, expectedQuery)
			}

			return nil
		})
		defer utils.ResetExecOnDatabase()

		conn := &utils.DBConnWithContext{}
		err := hub.CreateDatabase(conn, "testdb")
		if err != nil {
			t.Fatalf("unexpected error: %#v", err)
		}
	})

	t.Run("fails to create the database", func(t *testing.T) {
		expectedErr := errors.New("error")
		utils.SetExecOnDatabase(func(conn *utils.DBConnWithContext, dbname, query string) error {
			return expectedErr
		})
		defer utils.ResetExecOnDatabase()

		conn := &utils.DBConnWithContext{}
		err := hub.CreateDatabase(conn, "testdb")
		if !errors.Is(err, expectedErr) {
			t.Fatalf("got %#v, want %#v", err, expectedErr)
		}
	})
}

func TestSetGpUserPasswd(t *testing.T) {
	testhelper.SetupTestLogger()

	t.Run("succesfully sets the database user password", func(t *testing.T) {
		utils.System.CurrentUser = func() (*user.User, error) {
			return &user.User{Username: "gpadmin"}, nil
		}
		defer utils.ResetSystemFunctions()

		utils.SetExecOnDatabase(func(conn *utils.DBConnWithContext, dbname, query string) error {
			expectedQuery := `ALTER USER "gpadmin" WITH PASSWORD 'abc'`
			if query != expectedQuery {
				t.Fatalf("got %v, want %v", query, expectedQuery)
			}

			return nil
		})
		defer utils.ResetExecOnDatabase()

		conn := &utils.DBConnWithContext{}
		err := hub.SetGpUserPasswd(conn, "abc")
		if err != nil {
			t.Fatalf("unexpected error: %#v", err)
		}
	})

	t.Run("errors out when not able the get the current user", func(t *testing.T) {
		expectedErr := errors.New("error")
		utils.System.CurrentUser = func() (*user.User, error) {
			return nil, expectedErr
		}
		defer utils.ResetSystemFunctions()

		conn := &utils.DBConnWithContext{}
		err := hub.SetGpUserPasswd(conn, "abc")
		if !errors.Is(err, expectedErr) {
			t.Fatalf("got %#v, want %#v", err, expectedErr)
		}
	})

	t.Run("fails to set the database user password", func(t *testing.T) {
		expectedErr := errors.New("error")
		utils.System.CurrentUser = func() (*user.User, error) {
			return &user.User{Username: "gpadmin"}, nil
		}
		defer utils.ResetSystemFunctions()

		utils.SetExecOnDatabase(func(conn *utils.DBConnWithContext, dbname, query string) error {
			return expectedErr
		})
		defer utils.ResetExecOnDatabase()

		conn := &utils.DBConnWithContext{}
		err := hub.SetGpUserPasswd(conn, "abc")
		if !errors.Is(err, expectedErr) {
			t.Fatalf("got %#v, want %#v", err, expectedErr)
		}
	})
}

func segmentToProto(seg greenplum.Segment) *idl.Segment {
	return &idl.Segment{
		Port:          int32(seg.Port),
		DataDirectory: seg.DataDir,
		HostAddress:   seg.Address,
		HostName:      seg.Hostname,
	}
}