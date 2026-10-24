{{- define "victoriametrics.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "victoriametrics.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{- else }}{{ printf "%s-%s" .Release.Name (include "victoriametrics.name" .) | trunc 63 | trimSuffix "-" }}{{- end }}
{{- end }}
{{- define "victoriametrics.labels" -}}
app.kubernetes.io/name: {{ include "victoriametrics.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: observability-lab
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "victoriametrics.selectorLabels" -}}
app.kubernetes.io/name: {{ include "victoriametrics.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
