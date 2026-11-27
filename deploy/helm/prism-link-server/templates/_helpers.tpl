{{- define "prism-link-server.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "prism-link-server.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "prism-link-server.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "prism-link-server.selectorLabels" -}}
app.kubernetes.io/name: {{ include "prism-link-server.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "prism-link-server.labels" -}}
helm.sh/chart: {{ include "prism-link-server.chart" . }}
{{ include "prism-link-server.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: link-server
{{- end }}

{{- define "prism-link-server.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "prism-link-server.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "prism-link-server.tokensSecret" -}}
{{- if .Values.auth.tokens.existingSecret }}
{{- .Values.auth.tokens.existingSecret }}
{{- else }}
{{- printf "%s-tokens" (include "prism-link-server.fullname" .) }}
{{- end }}
{{- end }}

{{- define "prism-link-server.pvcName" -}}
{{- if .Values.storage.dir.existingClaim }}
{{- .Values.storage.dir.existingClaim }}
{{- else }}
{{- printf "%s-data" (include "prism-link-server.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Refuses configurations the server cannot run safely. Called from the
Deployment so every render goes through it.
*/}}
{{- define "prism-link-server.validate" -}}
{{- $mode := .Values.storage.mode }}
{{- if not (has $mode (list "dir" "s3" "azure" "gcs")) }}
{{- fail (printf "storage.mode must be dir, s3, azure or gcs, not %q" $mode) }}
{{- end }}
{{- if and (gt (int .Values.replicaCount) 1) (eq $mode "dir") }}
{{- fail (printf "replicaCount=%d needs shared storage (storage.mode=s3, azure or gcs): the filesystem store (storage.mode=dir) serves one replica only" (int .Values.replicaCount)) }}
{{- end }}
{{- if and (eq $mode "s3") (not .Values.storage.s3.bucket) }}
{{- fail "storage.mode=s3 needs storage.s3.bucket" }}
{{- end }}
{{- if and (eq $mode "azure") (not .Values.storage.azure.url) }}
{{- fail "storage.mode=azure needs storage.azure.url (https://<account>.blob.core.windows.net/<container>[/prefix])" }}
{{- end }}
{{- if and (eq $mode "gcs") (not .Values.storage.gcs.bucket) }}
{{- fail "storage.mode=gcs needs storage.gcs.bucket" }}
{{- end }}
{{- if and (not .Values.auth.noAuth) (not .Values.auth.tokens.existingSecret) (not .Values.auth.tokens.file) }}
{{- fail "no credentials: set auth.tokens.existingSecret (a Secret holding tokens.json) or auth.tokens.file, or auth.noAuth=true for a closed network" }}
{{- end }}
{{- if and .Values.auth.tokens.existingSecret .Values.auth.tokens.file }}
{{- fail "set auth.tokens.existingSecret or auth.tokens.file, not both" }}
{{- end }}
{{- if and .Values.tls.enabled (not .Values.tls.existingSecret) }}
{{- fail "tls.enabled needs tls.existingSecret (a kubernetes.io/tls Secret)" }}
{{- end }}
{{- if and .Values.tls.clientCA.existingSecret (not .Values.tls.enabled) }}
{{- fail "tls.clientCA (mTLS) needs tls.enabled=true" }}
{{- end }}
{{- end }}

{{- define "prism-link-server.args" -}}
- link-server
{{- if eq .Values.storage.mode "s3" }}
- --s3
- {{ printf "s3://%s/%s" .Values.storage.s3.bucket (.Values.storage.s3.prefix | default "" | trimPrefix "/") | trimSuffix "/" | quote }}
{{- with .Values.storage.s3.endpoint }}
- --s3-endpoint
- {{ . | quote }}
{{- end }}
{{- with .Values.storage.s3.region }}
- --s3-region
- {{ . | quote }}
{{- end }}
{{- if .Values.storage.s3.pathStyle }}
- --s3-path-style
{{- end }}
{{- else if eq .Values.storage.mode "azure" }}
- --azure
- {{ .Values.storage.azure.url | quote }}
{{- if .Values.storage.azure.pathStyle }}
- --azure-path-style
{{- end }}
{{- else if eq .Values.storage.mode "gcs" }}
- --gcs
- {{ printf "gs://%s/%s" .Values.storage.gcs.bucket (.Values.storage.gcs.prefix | default "" | trimPrefix "/") | trimSuffix "/" | quote }}
{{- with .Values.storage.gcs.endpoint }}
- --gcs-endpoint
- {{ . | quote }}
{{- end }}
{{- else }}
- --dir
- /data
{{- end }}
{{- if .Values.auth.noAuth }}
- --no-auth
{{- else }}
- --tokens
- {{ printf "/etc/prism/tokens/%s" .Values.auth.tokens.key | quote }}
{{- end }}
- --port
- {{ .Values.server.port | quote }}
- --ops-addr
- {{ printf ":%v" .Values.server.opsPort | quote }}
{{- if .Values.tls.enabled }}
- --tls-cert
- /etc/prism/tls/tls.crt
- --tls-key
- /etc/prism/tls/tls.key
{{- if .Values.tls.clientCA.existingSecret }}
- --client-ca
- {{ printf "/etc/prism/client-ca/%s" .Values.tls.clientCA.key | quote }}
{{- end }}
{{- end }}
- --keep
- {{ .Values.server.keep | quote }}
- --refresh
- {{ .Values.server.refresh | quote }}
- --rate
- {{ .Values.server.rate | quote }}
- --max-conns
- {{ .Values.server.maxConns | quote }}
- --max-bundle-mb
- {{ .Values.server.maxBundleMB | quote }}
- --audit-log
- {{ .Values.server.auditLog | quote }}
{{- if .Values.server.trustProxy }}
- --trust-proxy
{{- end }}
{{- range .Values.server.extraArgs }}
- {{ . | quote }}
{{- end }}
{{- end }}

{{/*
GOMEMLIMIT: "auto" is 80% of resources.limits.memory given in Mi or Gi.
*/}}
{{- define "prism-link-server.goMemLimit" -}}
{{- $v := .Values.goMemLimit | default "" -}}
{{- if eq $v "auto" -}}
{{- $lim := (.Values.resources.limits | default dict).memory | default "" | toString -}}
{{- if hasSuffix "Gi" $lim -}}
{{- printf "%dMiB" (div (mul (trimSuffix "Gi" $lim | int) 1024 8) 10) -}}
{{- else if hasSuffix "Mi" $lim -}}
{{- printf "%dMiB" (div (mul (trimSuffix "Mi" $lim | int) 8) 10) -}}
{{- end -}}
{{- else -}}
{{- $v -}}
{{- end -}}
{{- end }}
