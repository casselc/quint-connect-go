package fleetrollout

import (
	"fmt"
	"strings"
	"time"

	"github.com/casselc/quint-connect-go/sktrace"
)

// KwokStages are kwok Stage resources (kwok.x-k8s.io/v1alpha1) for pods,
// timed by a production trace: the kwok tier's counterpart of
// Config.PodTimes. kwok's default pod stages (kustomize/stage/pod/fast)
// make a pod ready, complete a Job's pod and delete a pod at once; these
// keep their selectors and status templates and add the delays.
//
// Ready are the options of the time a pod takes to become ready, Complete
// of the time a Job's pod runs once ready (nil: at once, as kwok's
// default). Each option is a stage of its own with weight 1: kwok picks
// one of the stages a pod matches by weight, so every option is equally
// likely, as kubesim's choice among PodTimes is. The YAML is a stream of
// documents for `kwokctl create cluster --config`; giving any pod stage
// replaces kwok's defaults for pods (its node stages stay).
func KwokStages(ready, complete []time.Duration) string {
	var b strings.Builder
	stages(&b, "pod-ready", readySelector, readyStatus, ready)
	stages(&b, "pod-complete", completeSelector, completeStatus, complete)
	b.WriteString(podDelete)
	return b.String()
}

// KwokStagesFromTrace times pods by the trace's lifetimes (of pods owned by
// objects of kind gvk, "": any): n quantiles, speed times faster, for both
// readiness and a Job's run. A trace with no ended pod is an error: the
// stages would be kwok's defaults, which a test meant to be timed by the
// trace should not get silently.
func KwokStagesFromTrace(tr *sktrace.Trace, gvk string, n int, speed float64) (string, error) {
	if speed <= 0 {
		speed = 1
	}
	qs := sktrace.Quantiles(tr.Lifetimes(gvk), n)
	if len(qs) == 0 {
		return "", fmt.Errorf("kwok stages: the trace has no ended pods owned by %q (n=%d): pick another kind (\"\": any) or trace", gvk, n)
	}
	for i, d := range qs {
		qs[i] = time.Duration(float64(d) / speed)
	}
	return KwokStages(qs, qs), nil
}

func stages(b *strings.Builder, name, selector, status string, delays []time.Duration) {
	if len(delays) == 0 {
		fmt.Fprintf(b, "---\napiVersion: kwok.x-k8s.io/v1alpha1\nkind: Stage\nmetadata:\n  name: %s\nspec:\n%s%s", name, selector, status)
		return
	}
	for i, d := range delays {
		fmt.Fprintf(b, "---\napiVersion: kwok.x-k8s.io/v1alpha1\nkind: Stage\nmetadata:\n  name: %s-%d\nspec:\n%s  weight: 1\n", name, i, selector)
		if ms := d.Milliseconds(); ms > 0 {
			fmt.Fprintf(b, "  delay:\n    durationMilliseconds: %d\n", ms)
		}
		b.WriteString(status)
	}
}

// The selectors and status templates below are kwok v0.7.0's
// kustomize/stage/pod/fast, unchanged.

const readySelector = `  resourceRef:
    apiGroup: v1
    kind: Pod
  selector:
    matchExpressions:
    - key: '.metadata.deletionTimestamp'
      operator: 'DoesNotExist'
    - key: '.status.podIP'
      operator: 'DoesNotExist'
`

const readyStatus = `  next:
    statusTemplate: |
      {{ $now := Now }}

      conditions:
      - lastTransitionTime: {{ $now | Quote }}
        status: "True"
        type: Initialized
      - lastTransitionTime: {{ $now | Quote }}
        status: "True"
        type: Ready
      - lastTransitionTime: {{ $now | Quote }}
        status: "True"
        type: ContainersReady
      {{ range .spec.readinessGates }}
      - lastTransitionTime: {{ $now | Quote }}
        status: "True"
        type: {{ .conditionType | Quote }}
      {{ end }}

      containerStatuses:
      {{ range .spec.containers }}
      - image: {{ .image | Quote }}
        name: {{ .name | Quote }}
        ready: true
        restartCount: 0
        state:
          running:
            startedAt: {{ $now | Quote }}
      {{ end }}

      initContainerStatuses:
      {{ range .spec.initContainers }}
      - image: {{ .image | Quote }}
        name: {{ .name | Quote }}
        ready: true
        restartCount: 0
        {{ if eq .restartPolicy "Always" }}
        started: true
        state:
          running:
            startedAt: {{ $now | Quote }}
        {{ else }}
        state:
          terminated:
            exitCode: 0
            finishedAt: {{ $now | Quote }}
            reason: Completed
            startedAt: {{ $now | Quote }}
        {{ end }}
      {{ end }}

      hostIP: {{ NodeIPWith .spec.nodeName | Quote }}
      podIP: {{ PodIPWith .spec.nodeName ( or .spec.hostNetwork false ) ( or .metadata.uid "" ) ( or .metadata.name "" ) ( or .metadata.namespace "" ) | Quote }}
      phase: Running
      startTime: {{ $now | Quote }}
`

const completeSelector = `  resourceRef:
    apiGroup: v1
    kind: Pod
  selector:
    matchExpressions:
    - key: '.metadata.deletionTimestamp'
      operator: 'DoesNotExist'
    - key: '.status.phase'
      operator: 'In'
      values:
      - 'Running'
    - key: '.metadata.ownerReferences.[].kind'
      operator: 'In'
      values:
      - 'Job'
`

const completeStatus = `  next:
    statusTemplate: |
      {{ $now := Now }}
      {{ $root := . }}
      containerStatuses:
      {{ range $index, $item := .spec.containers }}
      {{ $origin := index $root.status.containerStatuses $index }}
      - image: {{ $item.image | Quote }}
        name: {{ $item.name | Quote }}
        ready: false
        restartCount: 0
        started: false
        state:
          terminated:
            exitCode: 0
            finishedAt: {{ $now | Quote }}
            reason: Completed
            startedAt: {{ $now | Quote }}
      {{ end }}
      phase: Succeeded
`

const podDelete = `---
apiVersion: kwok.x-k8s.io/v1alpha1
kind: Stage
metadata:
  name: pod-delete
spec:
  resourceRef:
    apiGroup: v1
    kind: Pod
  selector:
    matchExpressions:
    - key: '.metadata.deletionTimestamp'
      operator: 'Exists'
  next:
    finalizers:
      empty: true
    delete: true
`
