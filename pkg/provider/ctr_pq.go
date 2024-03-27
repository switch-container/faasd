package provider

// this is a priority queue based on instance.lastActive
type CtrFreePQ []*CtrInstance

func NewCtrFreePQ() CtrFreePQ {
	return []*CtrInstance{}
}

func (pq CtrFreePQ) Len() int { return len(pq) }

func (pq CtrFreePQ) Less(i, j int) bool {
	return pq[i].lastActive.Before(pq[j].lastActive)
}

func (pq CtrFreePQ) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].localPQIndex = i
	pq[j].localPQIndex = j
}

func (pq *CtrFreePQ) Push(x any) {
	n := len(*pq)
	item := x.(*CtrInstance)
	item.localPQIndex = n
	*pq = append(*pq, item)
}

func (pq *CtrFreePQ) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil         // avoid memory leak
	item.localPQIndex = -1 // for safety
	*pq = old[0 : n-1]
	return item
}

type GlobalFreePQ []*CtrInstance

func NewGloablFreePQ() GlobalFreePQ {
	return []*CtrInstance{}
}

func (pq GlobalFreePQ) Len() int { return len(pq) }

func (pq GlobalFreePQ) Less(i, j int) bool {
	return pq[i].lastActive.Before(pq[j].lastActive)
}

func (pq GlobalFreePQ) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].globalPQIndex = i
	pq[j].globalPQIndex = j
}

func (pq *GlobalFreePQ) Push(x any) {
	n := len(*pq)
	item := x.(*CtrInstance)
	item.globalPQIndex = n
	*pq = append(*pq, item)
}

func (pq *GlobalFreePQ) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil          // avoid memory leak
	item.globalPQIndex = -1 // for safety
	*pq = old[0 : n-1]
	return item
}
