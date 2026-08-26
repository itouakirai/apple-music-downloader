package runv4

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type template struct {
	ctx   []u32
	st    St
	entry Round1Regs
}

type templateResponse struct {
	Ctx   string `json:"ctx"`
	State string `json:"state"`
	RCX   string `json:"rcx"`
	RAX   string `json:"rax"`
	RDX   string `json:"rdx"`
	R9    string `json:"r9"`
	RBP   string `json:"rbp"`
}

type liteResponse struct {
	Code int              `json:"code"`
	Msg  string           `json:"msg"`
	Data templateResponse `json:"data"`
}

func fetchTemplate(baseURL, adam, uri string) (*template, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/key?adamId=" + url.QueryEscape(adam) + "&uri=" + url.QueryEscape(uri)
	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lite-server /key returned %s", resp.Status)
	}
	var envelope liteResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Code != 0 {
		return nil, fmt.Errorf("lite-server /key returned code=%d msg=%s", envelope.Code, envelope.Msg)
	}
	data := envelope.Data
	ctxRaw, err := base64.StdEncoding.DecodeString(data.Ctx)
	if err != nil || len(ctxRaw) < 0x8000 {
		return nil, errors.New("invalid ctx in lite-server response")
	}
	stateRaw, err := base64.StdEncoding.DecodeString(data.State)
	if err != nil || len(stateRaw) < 0x2000 {
		return nil, errors.New("invalid state in lite-server response")
	}
	parse := func(value string) (u32, error) {
		value = strings.TrimPrefix(strings.TrimSpace(value), "0x")
		n, err := strconv.ParseUint(value, 16, 64)
		return u32(n), err
	}
	rcx, err := parse(data.RCX)
	if err != nil {
		return nil, err
	}
	rax, err := parse(data.RAX)
	if err != nil {
		return nil, err
	}
	rdx, err := parse(data.RDX)
	if err != nil {
		return nil, err
	}
	r9, err := parse(data.R9)
	if err != nil {
		return nil, err
	}
	rbp, err := parse(data.RBP)
	if err != nil {
		return nil, err
	}

	tmpl := &template{ctx: make([]u32, len(ctxRaw))}
	for i, b := range ctxRaw {
		tmpl.ctx[i] = u32(b)
	}
	for offset := 0; offset < stSize; offset++ {
		pos := 0x2000 - offset
		tmpl.st[offset] = u32(binary.LittleEndian.Uint32(stateRaw[pos : pos+4]))
	}
	tmpl.entry = Round1Regs{rdx: rdx, rcx: rcx, rax: rax, r9: r9, rbp: rbp}
	return tmpl, nil
}

func decryptSample(tmpl *template, sample []byte) []byte {
	out := make([]byte, len(sample))
	copy(out[len(sample)/16*16:], sample[len(sample)/16*16:])
	ct := make([]u32, len(sample))
	for i, b := range sample {
		ct[i] = u32(b)
	}
	st := tmpl.st
	for block := 0; block < len(sample)/16; block++ {
		regs := tmpl.entry
		regs.rdi = u32(0x1EB2C6B4) ^ (u32(block) << 4)
		regs.rsi = u32(8) + (u32(block) << 4)
		mid := round1Mid(tmpl.ctx, &st, ct, &regs)
		r2 := round1Tail(tmpl.ctx, &st, mid.rax, mid.r13&0xff, mid.r15&0xff, mid.r8&0xff, mid.r14&0xff)
		r2v := round2Sub6400(tmpl.ctx, &st, r2.rdi, r2.rsi, r2.rdx, r2.rcx, r2.r8, r2.r9, r2.rax, r2.rbx, r2.r10, r2.r11, r2.r13, r2.r14, r2.r15, 0)
		r8p := r2v.cp12[2] ^ st[0x5b8] ^ r2v.cp12[1]
		v6 := t4(tmpl.ctx, 0x46a0, st[0x390]^0x2b) ^ st[0x5f0] ^ t4(tmpl.ctx, 0x4ac0, (r8p>>24)^0x29) ^ t4(tmpl.ctx, 0x2ff0, (st[0x298]>>16)^0xd6)
		v9 := t4(tmpl.ctx, 0x4ac0, r2v.cp12[0]) ^ st[0x540] ^ t4(tmpl.ctx, 0x2ff0, (r2v.v171>>16)^0x69)
		v11 := t4(tmpl.ctx, 0x3950, st[0x298]^0x57) ^ st[0x538] ^ t4(tmpl.ctx, 0x46a0, (r8p>>8)^0x2f)
		r3 := round3Sub8000(tmpl.ctx, &st, st[0x270], r2v.v189, r8p&0xff, (r8p>>16)&0xffff, st[0x280], v6, (r8p>>16)&0xff, v9, r2v.v187&0xff, v11, r2v.v189)
		base := r3.pt[0].offset
		for _, pair := range r3.pt {
			if pair.offset < base {
				base = pair.offset
			}
		}
		for _, pair := range r3.pt {
			index := pair.offset - base
			if index < 16 {
				out[block*16+int(index)] = byte(pair.value)
			}
		}
		st[0x108] = st[0x180]
		st[0x220] += 0x10
	}
	return out
}
