// Package godeploycfn has some helper functions
package godeploycfn

import (
	"testing"
)

func Test_CreateStackName(t *testing.T) {
	type args struct {
		alarmName string
	}

	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Test Global Alarm Name Generation",
			args: args{
				alarmName: "AWS/Lambda/Global/ConcurrentExecutions",
			},
			want: "infra-monitor-alerts-aws-lambda-global-concurrentexecutions",
		},
		{
			name: "Test Lambda Alarm Name Generation 2nd Case",
			args: args{
				alarmName: "AWS/Lambda/Global/Error",
			},
			want: "infra-monitor-alerts-aws-lambda-global-error",
		},
		{
			name: "Test Lambda Alarm Name Generation No Periods",
			args: args{
				alarmName: "AWS/Lambda/Global/Spot.foo.bar",
			},
			want: "infra-monitor-alerts-aws-lambda-global-spot-foo-bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CreateStackName(tt.args.alarmName); got != tt.want {
				t.Errorf("createStackName() = %v, want %v", got, tt.want)
			}
		})
	}
}
