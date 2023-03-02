package deployer

import (
	f5_bigip "github.com/zongzw/f5-bigip-rest/bigip"
	"github.com/zongzw/f5-bigip-rest/utils"
)

func deploy(bc *f5_bigip.BIGIPContext, partition string, ocfgs, ncfgs *map[string]interface{}) error {
	defer utils.TimeItToPrometheus()()

	cmds, err := bc.GenRestRequests(partition, ocfgs, ncfgs)
	if err != nil {
		return err
	}
	return bc.DoRestRequests(cmds)
}

func Deployer(stopCh chan struct{}, bigips []*f5_bigip.BIGIP) chan DeployRequest {
	pendingDeploys := make(chan DeployRequest, 16)
	go func() {
		for {
			select {
			case <-stopCh:
				close(pendingDeploys)
				return
			case r := <-pendingDeploys:
				slog := utils.LogFromContext(r.Context)
				slog.Debugf("Processing request: %s", r.Meta)
				done := make(chan bool)
				for _, bigip := range bigips {
					specified := r.Context.Value(CtxKey_SpecifiedBIGIP)
					if specified != nil && specified.(string) != bigip.URL {
						continue
					}
					bc := &f5_bigip.BIGIPContext{BIGIP: *bigip, Context: r.Context}
					go func(bc *f5_bigip.BIGIPContext, r DeployRequest) {
						defer func() { done <- true }()

						if r.Context.Value(CtxKey_CreatePartition) != nil {
							if err := bc.DeployPartition(r.Partition); err != nil {
								slog.Errorf("failed to deploy partition %s: %s", r.Partition, err.Error())
								return
							}
						}
						err := deploy(bc, r.Partition, r.From, r.To)
						if err != nil {
							// report the error to status or ...
							slog.Errorf("failed to do deployment to %s: %s", bc.URL, err.Error())
							return
						}
						if r.Context.Value(CtxKey_DeletePartition) != nil {
							if err := bc.DeletePartition(r.Partition); err != nil {
								slog.Errorf("failed to deploy partition %s: %s", r.Partition, err.Error())
								return
							}
						}
					}(bc, r)
				}
				for _, bigip := range bigips {
					specified := r.Context.Value(CtxKey_SpecifiedBIGIP)
					if specified != nil && specified.(string) != bigip.URL {
						continue
					}
					<-done
				}
			}
		}
	}()
	return pendingDeploys
}