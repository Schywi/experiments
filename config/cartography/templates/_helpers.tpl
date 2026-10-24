{{- define "cartography.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "cartography.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{- else }}{{ printf "%s-%s" .Release.Name (include "cartography.name" .) | trunc 63 | trimSuffix "-" }}{{- end }}
{{- end }}
{{- define "cartography.labels" -}}
app.kubernetes.io/name: {{ include "cartography.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: experiments-platform
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "cartography.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cartography.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "cartography.neo4jSelectorLabels" -}}
app.kubernetes.io/name: {{ include "cartography.name" . }}-neo4j
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
