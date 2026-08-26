package service

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestConvertSimpleChangeParams(t *testing.T) {
	tests := []struct {
		content string
		taskID  string
		action  string
		index   int
	}{
		{content: "task-1 U1", taskID: "task-1", action: constant.MjActionUpscale, index: 1},
		{content: "task-2 v4", taskID: "task-2", action: constant.MjActionVariation, index: 4},
		{content: "task-3 R", taskID: "task-3", action: constant.MjActionReRoll},
		{content: "  task-4   u2  ", taskID: "task-4", action: constant.MjActionUpscale, index: 2},
	}

	for _, test := range tests {
		t.Run(test.content, func(t *testing.T) {
			result := ConvertSimpleChangeParams(test.content)
			require.NotNil(t, result)
			require.Equal(t, test.taskID, result.TaskId)
			require.Equal(t, test.action, result.Action)
			require.Equal(t, test.index, result.Index)
		})
	}
}

func TestConvertSimpleChangeParamsRejectsMalformedInputWithoutPanicking(t *testing.T) {
	for _, content := range []string{
		"",
		"task ",
		"task u",
		"task v",
		"task u5",
		"task u1 extra",
	} {
		t.Run(content, func(t *testing.T) {
			require.NotPanics(t, func() {
				require.Nil(t, ConvertSimpleChangeParams(content))
			})
		})
	}
}

func TestCoverPlusActionToNormalActionRejectsMalformedCustomIDWithoutPanicking(t *testing.T) {
	for _, customID := range []string{
		"x",
		"MJ::JOB",
		"MJ::::",
		"MJ::JOB::upsample",
		"MJ::JOB::variation",
		"MJ::JOB::upsample::not-an-index",
	} {
		t.Run(customID, func(t *testing.T) {
			require.NotPanics(t, func() {
				err := CoverPlusActionToNormalAction(&dto.MidjourneyRequest{CustomId: customID})
				require.NotNil(t, err)
			})
		})
	}
}

func TestCoverPlusActionToNormalActionParsesIndexedActions(t *testing.T) {
	request := &dto.MidjourneyRequest{CustomId: "MJ::JOB::upsample::2::task-1"}
	require.Nil(t, CoverPlusActionToNormalAction(request))
	require.Equal(t, constant.MjActionUpscale, request.Action)
	require.Equal(t, 2, request.Index)
}
