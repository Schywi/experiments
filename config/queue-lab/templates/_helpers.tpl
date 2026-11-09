{{/* Labels the experiment's objects share, so a leaked object is attributable. */}}
{{- define "queue-lab.labels" -}}
app.kubernetes.io/name: queue-lab
app.kubernetes.io/part-of: queue-experiment
app.kubernetes.io/managed-by: helm
{{- end -}}

{{- define "queue-lab.selector" -}}
app.kubernetes.io/name: queue-lab
{{- end -}}
