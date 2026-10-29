package windowsupdate

// Propriedade da politica de Windows Update escrita pelo agente (WU-16/D04).
//
// O modo "Discovery como fonte unica" grava NoAutoUpdate=1 na arvore de
// Policies para silenciar o Windows Update nativo. Escrever em Policies de um
// cliente exige cuidado: um GPO de administrador pode ja existir, e reverter
// sem saber de quem e o valor destruiria configuracao alheia.
//
// Contrato (modelo Breeze): o agente marca o que escreveu com sentinelas e so
// reverte o que e dele.
//  - DiscoveryManagedNoAutoUpdate=1 marca o NoAutoUpdate como nosso;
//  - DiscoveryCreatedAUKey=1 marca que a chave AU foi criada por nos (permite
//    remove-la no revert se ficar vazia).
//
// A decisao pura vive aqui (sem build tag) para rodar no CI Linux.
const (
	// discoveryManagedValue marca NoAutoUpdate como escrito pelo Discovery.
	discoveryManagedValue = "DiscoveryManagedNoAutoUpdate"
	// discoveryCreatedKeyValue marca que o Discovery criou a chave AU.
	discoveryCreatedKeyValue = "DiscoveryCreatedAUKey"
)

// PolicyWriteAllowed decide se o agente pode sobrescrever NoAutoUpdate.
//
// Regras:
//  • valor ausente → pode escrever (nada a preservar);
//  • valor presente e sentinela nossa → pode reescrever (e nosso);
//  • valor presente e sentinela ausente → NAO escrever: e GPO/administrador.
func PolicyWriteAllowed(existingNoAutoUpdate *uint32, discoveryManaged bool) bool {
	if existingNoAutoUpdate == nil {
		return true
	}
	return discoveryManaged
}

// PolicyRevertDecision descreve o que o revert deve fazer com a chave AU.
type PolicyRevertDecision struct {
	// DeleteValues sao os valores que o agente escreveu e deve remover.
	DeleteValues []string
	// DeleteKey indica remover a chave AU (somente se criada por nos e vazia).
	DeleteKey bool
}

// PlanPolicyRevert monta o plano de revert a partir do estado observado.
func PlanPolicyRevert(
	noAutoUpdatePresent bool,
	discoveryManaged bool,
	keyCreatedByUs bool,
	remainingValues int,
	remainingSubKeys int) PolicyRevertDecision {
	decision := PolicyRevertDecision{}

	if discoveryManaged {
		decision.DeleteValues = append(decision.DeleteValues, "NoAutoUpdate", discoveryManagedValue)
		if keyCreatedByUs {
			decision.DeleteValues = append(decision.DeleteValues, discoveryCreatedKeyValue)
		}
	}

	// So remove a chave quando ELA foi criada por nos e ficou vazia.
	if keyCreatedByUs && discoveryManaged && remainingValues == 0 && remainingSubKeys == 0 {
		decision.DeleteKey = true
	}
	_ = noAutoUpdatePresent
	return decision
}
