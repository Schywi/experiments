{{- define "vector.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "vector.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{- else }}{{ printf "%s-%s" .Release.Name (include "vector.name" .) | trunc 63 | trimSuffix "-" }}{{- end }}
{{- end }}
{{- define "vector.labels" -}}
app.kubernetes.io/name: {{ include "vector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: bounded-worm-lab
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "vector.selectorLabels" -}}
app.kubernetes.io/name: {{ include "vector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
