// Code generated manually for this small API. DO NOT EDIT without updating copies.
package v1alpha1

import "k8s.io/apimachinery/pkg/runtime"

func (in *Worm) DeepCopyInto(out *Worm) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Status.DeepCopyInto(&out.Status)
}
func (in *Worm) DeepCopy() *Worm {
	if in == nil {
		return nil
	}
	out := new(Worm)
	in.DeepCopyInto(out)
	return out
}
func (in *Worm) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
func (in *WormList) DeepCopyInto(out *WormList) {
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Worm, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}
func (in *WormList) DeepCopy() *WormList {
	if in == nil {
		return nil
	}
	out := new(WormList)
	in.DeepCopyInto(out)
	return out
}
func (in *WormList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
func (in *WormStatus) DeepCopyInto(out *WormStatus) {
	*out = *in
	if in.AcceptedIntentIDs != nil {
		out.AcceptedIntentIDs = append([]string(nil), in.AcceptedIntentIDs...)
	}
}
