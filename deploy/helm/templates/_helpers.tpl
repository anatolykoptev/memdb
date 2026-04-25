{{/*
Expand the name of the chart.
*/}}
{{- define "memdb.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "memdb.fullname" -}}
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

{{/*
Create chart label.
*/}}
{{- define "memdb.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "memdb.labels" -}}
helm.sh/chart: {{ include "memdb.chart" . }}
{{ include "memdb.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "memdb.selectorLabels" -}}
app.kubernetes.io/name: {{ include "memdb.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Component-specific selector labels.
Usage: {{ include "memdb.componentSelectorLabels" (dict "root" . "component" "memdb-go") }}
*/}}
{{- define "memdb.componentSelectorLabels" -}}
app.kubernetes.io/name: {{ include "memdb.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/*
Name of the existing secret that holds sensitive values.
*/}}
{{- define "memdb.secretName" -}}
{{- .Values.existingSecret | default (printf "%s-secrets" (include "memdb.fullname" .)) }}
{{- end }}

{{/*
Postgres DSN (cluster-internal).
*/}}
{{- define "memdb.postgresURL" -}}
{{- printf "postgresql://%s@%s-postgres:5432/%s" .Values.postgres.user (include "memdb.fullname" .) .Values.postgres.database }}
{{- end }}

{{/*
Redis URL (cluster-internal).
*/}}
{{- define "memdb.redisURL" -}}
{{- printf "redis://%s-redis:6379/1" (include "memdb.fullname" .) }}
{{- end }}

{{/*
Qdrant addr (cluster-internal, gRPC).
*/}}
{{- define "memdb.qdrantAddr" -}}
{{- printf "%s-qdrant:%d" (include "memdb.fullname" .) (.Values.qdrant.grpcPort | int) }}
{{- end }}

{{/*
Embed-server URL (cluster-internal).
*/}}
{{- define "memdb.embedURL" -}}
{{- printf "http://%s-embed-server:%d" (include "memdb.fullname" .) (.Values.embedServer.service.port | int) }}
{{- end }}
