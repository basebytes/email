package email

import (
	"bytes"
	"fmt"
	"net/mail"
	"net/textproto"
	"strings"
)

const (
	mimeVersion             = "1.0"
	encodingBase64          = "base64"
	encodingQuotedPrintable = "quoted-printable"
	contentTypeText         = "text/plain; charset=UTF-8"
	contentTypeHtml         = "text/html; charset=UTF-8"
	contentType8bit         = "application/octet-stream"
	contentTypeMixed        = "multipart/mixed"
	defaultMaxQueueSize     = 100
)

var crlf = []byte{'\r', '\n'}

type Config struct {
	Server       string
	User         string
	Password     string
	MaxQueueSize int
}

type Option func(params *params)

type Message struct {
	h textproto.MIMEHeader
	c string
}

func (m *Message) header() textproto.MIMEHeader {
	return m.h
}
func (m *Message) content() string {
	return m.c
}

type addressSlice []*mail.Address

func (a addressSlice) String() string {
	var addr []string
	for _, add := range a {
		addr = append(addr, add.String())
	}
	return strings.Join(addr, ",")
}

func (a addressSlice) Addr() (addr []string) {
	for _, add := range a {
		addr = append(addr, add.Address)
	}
	return addr
}

type params struct {
	user      *mail.Address
	headers   map[string]string
	addresses []string
}

func (p *params) Add(k, v string) {
	p.headers[k] = v
}

func (p *params) writeHeaders(buffer *bytes.Buffer) {
	for key, value := range p.headers {
		_, _ = fmt.Fprintf(buffer, "%s: %s%s", key, value, crlf)
	}
	buffer.Write(crlf)
}

type letter struct {
	addresses []string
	content   []byte
}

func newParams(user mail.Address, to addressSlice) *params {
	return &params{
		user:      &user,
		addresses: to.Addr(),
		headers: map[string]string{
			"From":         user.String(),
			"To":           to.String(),
			"Mime-Version": mimeVersion,
		},
	}
}
