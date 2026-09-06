{{- define "worm-regression.labels" -}}
app.kubernetes.io/name: worm-regression
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: bounded-worm-lab
{{- end -}}
{{- define "worm-regression.selectorLabels" -}}
app.kubernetes.io/name: worm-regression
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
