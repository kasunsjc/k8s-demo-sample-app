{{- define "kubeplay.image" -}}
{{- $reg := .root.Values.global.imageRegistry -}}
{{- if $reg }}{{ $reg }}/{{ end }}{{ .image }}:{{ .root.Values.global.imageTag }}
{{- end }}

{{- define "kubeplay.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/part-of: kubeplay
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
helm.sh/chart: {{ .root.Chart.Name }}-{{ .root.Chart.Version | replace "+" "_" }}
{{- end }}

{{- define "kubeplay.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: {{ . }}
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{- define "kubeplay.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end }}

{{- define "kubeplay.downwardEnv" -}}
- name: POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
- name: POD_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
- name: NODE_NAME
  valueFrom:
    fieldRef:
      fieldPath: spec.nodeName
{{- end }}

{{- define "kubeplay.redisEnv" -}}
- name: REDIS_HOST
  value: redis
- name: REDIS_PORT
  value: "6379"
- name: REDIS_PASSWORD
  valueFrom:
    secretKeyRef:
      name: redis-auth
      key: password
{{- end }}
