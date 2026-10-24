{{- define "laya.labels" -}}
app.kubernetes.io/name: laya
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: models-lab
{{- end -}}
{{- define "laya.selectorLabels" -}}
app.kubernetes.io/name: laya
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
