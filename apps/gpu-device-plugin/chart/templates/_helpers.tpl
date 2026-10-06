{{- define "gpu-device-plugin.labels" -}}
app.kubernetes.io/name: gpu-device-plugin
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: gpu-lab
{{- end -}}
{{- define "gpu-device-plugin.selectorLabels" -}}
app.kubernetes.io/name: gpu-device-plugin
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
