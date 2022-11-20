package f5_bigip

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gitee.com/zongzw/f5-bigip-rest/utils"
)

func (bip *BIGIP) DoRestRequests(rr *[]RestRequest) error {
	if transId, err := bip.MakeTrans(); err != nil {
		return err
	} else {
		if count, err := bip.DeployWithTrans(rr, transId); err != nil || count == 0 {
			return err
		} else {
			return bip.CommitTrans(transId)
		}
	}
}

func (bip *BIGIP) constructFolder(name, partition string) RestRequest {
	kind := "sys/folder"
	return RestRequest{
		Method: "NOPE",
		Body: map[string]interface{}{
			"name":      name,
			"partition": partition,
		},
		ResUri:    "/mgmt/tm/" + kind,
		Kind:      kind,
		ResName:   name,
		Partition: partition,
		Subfolder: "",
		WithTrans: true,
	}
}

func (bip *BIGIP) constructLTMRes(kind, name, partition, subfolder string, body interface{}) RestRequest {
	return RestRequest{
		Method:    "NOPE",
		Headers:   map[string]interface{}{},
		Body:      body,
		ResUri:    "/mgmt/tm/" + kind,
		Kind:      kind,
		ResName:   name,
		Partition: partition,
		Subfolder: subfolder,
		WithTrans: true,
	}
}

func (bip *BIGIP) constructNetRes(kind, name, partition, subfolder string, body interface{}) RestRequest {
	return RestRequest{
		Method:    "NOPE",
		Headers:   map[string]interface{}{},
		Body:      body,
		ResUri:    "/mgmt/tm/" + kind,
		Kind:      kind,
		ResName:   name,
		Partition: partition,
		Subfolder: subfolder,
		WithTrans: true,
	}
}

func (bip *BIGIP) constructSysRes(kind, name, partition, subfolder string, body interface{}) RestRequest {
	return RestRequest{
		Method:    "NOPE",
		Body:      body,
		Headers:   map[string]interface{}{},
		ResUri:    "/mgmt/tm/" + kind,
		Kind:      kind,
		ResName:   name,
		Partition: partition,
		Subfolder: subfolder,
		WithTrans: true,
	}
}

func (bip *BIGIP) constructSharedRes(kind, name, partition, subfolder string, body interface{}, operation string) (RestRequest, error) {
	r := RestRequest{}

	switch kind {
	case "shared/file-transfer/uploads":
		if operation == "deploy" {
			rawbody := body.(map[string]interface{})["content"].(string)
			size := len(rawbody)
			r = RestRequest{
				Method: "POST",
				Body:   rawbody,
				ResUri: "/mgmt/shared/file-transfer/uploads/" + name,
				Headers: map[string]interface{}{
					"Content-Type":   "application/octet-stream",
					"Content-Length": fmt.Sprintf("%d", size),
					"Content-Range":  fmt.Sprintf("0-%d/%d", size-1, size),
				},
				Partition: partition,
				Subfolder: subfolder,
				ResName:   name,
				Kind:      kind,
				WithTrans: false,
			}
		} else if operation == "delete" {
			// the uploaded file would be removed automatically by BIG-IP,
			// we needn't to handle it.
			r = RestRequest{
				ScheduleIt: "never",
				Method:     "POST",
				Body: map[string]interface{}{
					"command":     "run",
					"utilCmdArgs": fmt.Sprintf("-c 'rm -f /var/config/rest/downloads/%s'", name),
				},
				ResUri:    "/mgmt/tm/util/bash",
				Partition: partition,
				Subfolder: subfolder,
				ResName:   name,
				Kind:      kind,
				WithTrans: false,
			}
		}

	default:
		return r, fmt.Errorf("not supported kind %s", kind)
	}

	return r, nil
}

func (bip *BIGIP) GetExistingResources(partition string, kinds []string) (*map[string]map[string]interface{}, error) {
	defer utils.TimeItToPrometheus()()

	exists := map[string]map[string]interface{}{}
	partitions, err := bip.ListPartitions()
	if err != nil {
		return nil, fmt.Errorf("failed to list partitions for checking res existence: %s", err.Error())
	}
	if !utils.Contains(partitions, partition) {
		return &exists, nil
	}

	for _, kind := range kinds {
		if !(strings.HasPrefix(kind, "sys/") || strings.HasPrefix(kind, "ltm/") || strings.HasPrefix(kind, "net/")) {
			continue
		}
		exists[kind] = map[string]interface{}{}
		resp, err := bip.All(fmt.Sprintf("%s?$filter=partition+eq+%s", kind, partition))
		if err != nil {
			return nil, fmt.Errorf("failed to list '%s' of %s: %s", kind, partition, err.Error())
		}

		if items, ok := (*resp)["items"]; !ok {
			return nil, fmt.Errorf("failed to get items from response")
		} else {
			for _, item := range items.([]interface{}) {
				props := item.(map[string]interface{})
				p, f, n := partition, "", props["name"].(string)
				if ff, ok := props["subPath"]; ok {
					f = ff.(string)
				}
				exists[kind][utils.Keyname(p, f, n)] = props
			}
		}
	}
	return &exists, nil
}

func (bip *BIGIP) GenRestRequests(partition string, ocfg, ncfg *map[string]interface{}) (*[]RestRequest, error) {
	defer utils.TimeItToPrometheus()()

	rDels := map[string][]RestRequest{}
	rCrts := map[string][]RestRequest{}
	rDelFldrs := []RestRequest{}
	rCrtFldrs := []RestRequest{}

	kinds := GatherKinds(ocfg, ncfg)
	existings, err := bip.GetExistingResources(partition, kinds)
	if err != nil {
		return nil, err
	}
	if ocfg != nil {
		var err error
		if rDelFldrs, rDels, err = bip.cfg2RestRequests(partition, "delete", *ocfg, existings); err != nil {
			return &[]RestRequest{}, err
		}
	}
	if ncfg != nil {
		var err error
		if rCrtFldrs, rCrts, err = bip.cfg2RestRequests(partition, "deploy", *ncfg, existings); err != nil {
			return &[]RestRequest{}, err
		}
	}

	sweepcmds := func(dels, crts map[string][]RestRequest) ([]RestRequest, []RestRequest, []RestRequest) {
		c, d, u := []RestRequest{}, []RestRequest{}, []RestRequest{}
		for _, t := range ResOrder {
			rex := regexp.MustCompile(t)
			ks := []string{}
			for k := range dels {
				if rex.MatchString(k) {
					ks = append(ks, k)
				}
			}
			for k := range crts {
				if rex.MatchString(k) {
					ks = append(ks, k)
				}
			}
			ks = utils.Unified(ks)

			for _, k := range ks {
				drs, crs := []RestRequest{}, []RestRequest{}
				if rr, f := dels[k]; f {
					drs = rr
				}
				if rr, f := crts[k]; f {
					crs = rr
				}
				drmap := map[string]RestRequest{}
				for _, dr := range drs {
					drmap[utils.Keyname(dr.Partition, dr.Subfolder, dr.ResName)] = dr
				}
				for _, cr := range crs {
					jn := utils.Keyname(cr.Partition, cr.Subfolder, cr.ResName)
					if dr, f := drmap[jn]; f {
						same := utils.DeepEqual(
							dr.Body.(map[string]interface{}),
							cr.Body.(map[string]interface{}))
						needcreat := dr.Method == "NOPE"
						if needcreat {
							cr.Method = "POST"
							c = append(c, cr)
						} else {
							if !same {
								cr.Method = "PATCH"
								u = append(u, cr)
							} else {
								if expected := getFromExists(cr.Kind, cr.Partition, cr.Subfolder, cr.ResName, existings); expected != nil {
									if !utils.FieldsIsExpected(cr.Body, (*expected)) {
										cr.Method = "PATCH"
										u = append(u, cr)
									} else {
										// nothing, igore this RestRequest because all fields are expected.
									}
								} else {
									c = append(c, cr)
								}
							}
						}
						delete(drmap, jn)
					} else {
						c = append(c, cr)
					}
				}
				for _, dr := range drmap {
					d = append(d, dr)
				}
			}
		}

		// reverse the order of deletion
		dd := []RestRequest{}
		for _, dr := range d {
			dd = append([]RestRequest{dr}, dd...)
		}

		return c, dd, u
	}

	laycmds := func() []RestRequest {
		cmds := []RestRequest{}
		cf, df, _ := sweepcmds(
			map[string][]RestRequest{"sys/folder": rDelFldrs},
			map[string][]RestRequest{"sys/folder": rCrtFldrs},
		)

		vcmdDels, vcmdCrts := []RestRequest{}, []RestRequest{}
		// if there were virtual-address change ...
		// this 'if' block is used to handle the case of: virtual-address's name is not IP addr which
		// is deployed via AS3 ever before.
		// i.e.   "app_svc_vip": {
		// 			"class": "Service_Address",
		// 			"virtualAddress": "172.16.142.112",
		// 			"arpEnabled": true
		// 		  },
		// this case may happen in migration process
		if virtualAddressNameDismatched(append(rDels["ltm/virtual-address"], rCrts["ltm/virtual-address"]...)) {
			rDelVs := map[string][]RestRequest{
				"ltm/virtual":         rDels["ltm/virtual"],
				"ltm/virtual-address": rDels["ltm/virtual-address"],
			}
			rCrtVs := map[string][]RestRequest{
				"ltm/virtual":         rCrts["ltm/virtual"],
				"ltm/virtual-address": rCrts["ltm/virtual-address"],
			}
			cvl, dvl, uvl := sweepcmds(rDelVs, rCrtVs)
			if len(cvl)+len(dvl)+len(uvl) != 0 {
				delete(rDels, "ltm/virtual")
				delete(rDels, "ltm/virtual-address")
				delete(rCrts, "ltm/virtual")
				delete(rCrts, "ltm/virtual-address")
				vcmdDels = sortRestRequests(append(rDels["ltm/virtual"], rDels["ltm/virtual-address"]...), true)
				vcmdCrts = sortRestRequests(append(rCrts["ltm/virtual"], rCrts["ltm/virtual-address"]...), false)
				for i := range vcmdCrts {
					vcmdCrts[i].Method = "POST"
				}
			}
		}

		cl, dl, ul := sweepcmds(rDels, rCrts)

		cmds = append(cmds, cf...)
		cmds = append(cmds, cl...)
		cmds = append(cmds, ul...)
		cmds = append(cmds, dl...)
		cmds = append(cmds, vcmdDels...)
		cmds = append(cmds, vcmdCrts...)
		cmds = append(cmds, df...)

		// if there is virtual-address change...

		return cmds
	}

	cmds := laycmds()

	if bcmds, err := json.Marshal(cmds); err == nil {
		slog.Debugf("commands: %s", bcmds)
	}
	return &cmds, nil
}

func (bip *BIGIP) cfg2RestRequests(partition, operation string, cfg map[string]interface{}, exists *map[string]map[string]interface{}) ([]RestRequest, map[string][]RestRequest, error) {
	slog.Debugf("generating '%s' cmds for partition %s's config", operation, partition)
	rrs := map[string][]RestRequest{}

	rFldrs := []RestRequest{}
	for fn, ress := range cfg {
		if fn != "" {
			rSubfolder := bip.constructFolder(fn, partition)
			rSubfolder.Method = opr2method(operation, nil != getFromExists("sys/folder", partition, "", fn, exists))
			rFldrs = append(rFldrs, rSubfolder)
		}

		for tn, body := range ress.(map[string]interface{}) {
			tnarr := strings.Split(tn, "/")
			t := strings.Join(tnarr[0:len(tnarr)-1], "/")
			rootKind := tnarr[0]
			n := tnarr[len(tnarr)-1]
			var r RestRequest
			var err error = nil
			switch rootKind {
			case "ltm":
				r = bip.constructLTMRes(t, n, partition, fn, body)
				r.Method = opr2method(operation, nil != getFromExists(t, partition, fn, n, exists))
			case "net":
				r = bip.constructNetRes(t, n, partition, fn, body)
				r.Method = opr2method(operation, nil != getFromExists(t, partition, fn, n, exists))
			case "sys":
				r = bip.constructSysRes(t, n, partition, fn, body)
				r.Method = opr2method(operation, nil != getFromExists(t, partition, fn, n, exists))
			case "shared":
				r, err = bip.constructSharedRes(t, n, partition, fn, body, operation)
			default:
				return rFldrs, rrs, fmt.Errorf("not support root kind: %s", rootKind)
			}
			if err != nil {
				return rFldrs, rrs, err
			} else {
				if _, f := rrs[t]; !f {
					rrs[t] = []RestRequest{}
				}
				if r.ScheduleIt != "" {
					// TODO: add it to resSyncer
				} else {
					rrs[t] = append(rrs[t], r)
				}
			}
		}
	}
	return rFldrs, rrs, nil
}

func (bip *BIGIP) DeployPartition(name string) error {
	if name == "Common" {
		return nil
	}
	pobj, err := bip.Exist("sys/folder", "", name, "")
	if err != nil {
		return err
	}

	if pobj == nil {
		return bip.Deploy("sys/folder", name, "/", "", map[string]interface{}{})
	}
	return nil
}

func (bip *BIGIP) DeletePartition(name string) error {
	if name == "Common" {
		return nil
	}
	if f, err := bip.Exist("sys/folder", "", name, ""); err != nil {
		return err
	} else if f == nil {
		return nil
	}
	return bip.Delete("sys/folder", name, "", "")
}

func (bip *BIGIP) LoadDataGroup(dgkey string) (*PersistedConfig, error) {
	dgname := "f5-kic_" + dgkey
	resp, err := bip.Exist("ltm/data-group/internal", dgname, "cis-c-tenant", "")
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	if records, f := (*resp)["records"]; !f {
		return nil, fmt.Errorf("failed to get records field")
	} else {
		pc := PersistedConfig{}
		b64as3 := ""
		b64rest := ""
		b64psmap := ""
		for _, record := range records.([]interface{}) {
			mrec := record.(map[string]interface{})
			name := mrec["name"].(string)
			if name == "cmkey" {
				pc.CmKey = string(mrec["data"].(string))
			} else if strings.HasPrefix(name, "as3") {
				b64as3 += mrec["data"].(string)
			} else if strings.HasPrefix(name, "rest") {
				b64rest += mrec["data"].(string)
			} else if strings.HasPrefix(name, "psmap") {
				b64psmap += mrec["data"].(string)
			} else {
				return nil, fmt.Errorf("invalid unknown key: %s", name)
			}
		}
		if b64as3 != "" {
			if data, err := base64.StdEncoding.DecodeString(b64as3); err != nil {
				return nil, err
			} else {
				pc.AS3 = string(data)
			}
		}

		if b64rest != "" {
			if data, err := base64.StdEncoding.DecodeString(b64rest); err != nil {
				return nil, err
			} else {
				pc.Rest = string(data)
			}
		}

		if b64psmap != "" {
			if data, err := base64.StdEncoding.DecodeString(b64psmap); err != nil {
				return nil, err
			} else {
				var psm map[string]interface{}
				err := json.Unmarshal(data, &psm)
				if err != nil {
					return nil, err
				}
				pc.PsMap = psm
			}
		}

		return &pc, nil
	}
}

func (bip *BIGIP) SaveDataGroup(dgkey string, pc *PersistedConfig) error {
	dgname := "f5-kic_" + dgkey
	var err error
	// failed with error:  16908375, 01020057:3: The string with more than 65535 characters cannot be stored in a message.
	blocksize := 1024
	records := []interface{}{}

	resp, err := bip.Exist("ltm/data-group/internal", dgname, "cis-c-tenant", "")
	if err != nil {
		return err
	}

	if pc.CmKey != "" {
		records = append(records, map[string]string{
			"name": "cmkey",
			"data": pc.CmKey,
		})
	}

	if pc.AS3 != "" {
		b64as3 := base64.StdEncoding.EncodeToString([]byte(pc.AS3))
		bas3s := utils.Split(b64as3, blocksize)
		for i, d := range bas3s {
			records = append(records, map[string]string{
				"name": fmt.Sprintf("as3.%d", i),
				"data": d,
			})
		}
	}

	if pc.Rest != "" {
		b64rest := base64.StdEncoding.EncodeToString([]byte(pc.Rest))
		brests := utils.Split(b64rest, blocksize)
		for i, d := range brests {
			records = append(records, map[string]string{
				"name": fmt.Sprintf("rest.%d", i),
				"data": d,
			})
		}
	}

	if len(pc.PsMap) != 0 {
		bpsm, err := json.Marshal(pc.PsMap)
		if err != nil {
			return err
		}
		b64psm := base64.StdEncoding.EncodeToString(bpsm)
		bpsms := utils.Split(b64psm, blocksize)
		for i, d := range bpsms {
			records = append(records, map[string]string{
				"name": fmt.Sprintf("psmap.%d", i),
				"data": d,
			})
		}
	}

	body := map[string]interface{}{
		"name":      dgname,
		"type":      "string",
		"partition": "cis-c-tenant",
		"records":   records,
	}

	if resp == nil {
		err = bip.Deploy("ltm/data-group/internal", dgname, "cis-c-tenant", "", body)
	} else {
		err = bip.Update("ltm/data-group/internal", dgname, "cis-c-tenant", "", body)
	}
	return err
}

func (bip *BIGIP) DeleteDataGroup(dgkey string) error {
	dgname := "f5-kic_" + dgkey
	var err error
	resp, err := bip.Exist("ltm/data-group/internal", dgname, "cis-c-tenant", "")
	if err != nil {
		return err
	}
	if resp != nil {
		err = bip.Delete("ltm/data-group/internal", dgname, "cis-c-tenant", "")
	}
	return err
}

func (bip *BIGIP) ListPartitions() ([]string, error) {
	partitions := []string{}
	resp, err := bip.All("sys/folder")
	if err != nil {
		return partitions, fmt.Errorf("failed to list partitions: %s", err.Error())
	}

	if items, ok := (*resp)["items"]; !ok {
		return partitions, fmt.Errorf("failed to get items from response")
	} else {
		for _, item := range items.([]interface{}) {
			props := item.(map[string]interface{})
			if fullPath, f := props["fullPath"].(string); f {
				paths := strings.Split(fullPath, "/")
				if len(paths) == 2 && paths[1] != "" {
					partitions = append(partitions, paths[1])
				}
			}
		}
	}
	return utils.Unified(partitions), nil
}

func (bip *BIGIP) SaveSysConfig(partitions []string) error {
	cmd := "save sys config"
	if len(partitions) > 0 {
		cmd += "partitions { "

		for _, p := range partitions {
			cmd += p + " "
		}
		cmd += "}"
	}

	resp, err := bip.Tmsh(cmd)
	if err != nil {
		return err
	}
	if (*resp)["commandResult"] != nil {
		slog.Warnf("command %s: %v", cmd, (*resp)["commandResult"])
	}
	return nil
}
