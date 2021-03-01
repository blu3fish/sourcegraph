import React, { useCallback, useState } from 'react'
import * as H from 'history'
import Dialog from '@reach/dialog'
import { LoadingSpinner } from '@sourcegraph/react-loading-spinner'
import { Form } from '../../../../../branded/src/components/Form'
import { asError, isErrorLike } from '../../../../../shared/src/util/errors'
import { ErrorAlert } from '../../../components/alerts'
import { createCampaignsCredential as _createCampaignsCredential } from './backend'
import { ExternalServiceKind, Scalars } from '../../../graphql-operations'
import { defaultExternalServices } from '../../../components/externalServices/externalServices'
import classNames from 'classnames'
import { CodeHostSshPublicKey } from './CodeHostSshPublicKey'

export interface AddCredentialModalProps {
    onCancel: () => void
    afterCreate: () => void
    history: H.History
    userID: Scalars['ID']
    externalServiceKind: ExternalServiceKind
    externalServiceURL: string
    requiresSSH: boolean

    /** For testing only. */
    createCampaignsCredential?: typeof _createCampaignsCredential
    /** For testing only. */
    initialStep?: Step
}

const helpTexts: Record<ExternalServiceKind, JSX.Element> = {
    [ExternalServiceKind.GITHUB]: (
        <>
            <a
                href="https://docs.sourcegraph.com/campaigns/quickstart#configure-code-host-connections"
                rel="noreferrer noopener"
                target="_blank"
            >
                Create a new access token
            </a>{' '}
            with <code>repo</code>, <code>read:org</code>, <code>user:email</code>, and <code>read:discussion</code>{' '}
            scopes.
        </>
    ),
    [ExternalServiceKind.GITLAB]: (
        <>
            <a
                href="https://docs.sourcegraph.com/campaigns/quickstart#configure-code-host-connections"
                rel="noreferrer noopener"
                target="_blank"
            >
                Create a new access token
            </a>{' '}
            with <code>api</code>, <code>read_repository</code>, and <code>write_repository</code> scopes.
        </>
    ),
    [ExternalServiceKind.BITBUCKETSERVER]: (
        <>
            <a
                href="https://docs.sourcegraph.com/campaigns/quickstart#configure-code-host-connections"
                rel="noreferrer noopener"
                target="_blank"
            >
                Create a new access token
            </a>{' '}
            with <code>write</code> permissions on the project and repository level.
        </>
    ),

    // These are just for type completeness and serve as placeholders for a bright future.
    [ExternalServiceKind.BITBUCKETCLOUD]: <span>Unsupported</span>,
    [ExternalServiceKind.GITOLITE]: <span>Unsupported</span>,
    [ExternalServiceKind.PERFORCE]: <span>Unsupported</span>,
    [ExternalServiceKind.PHABRICATOR]: <span>Unsupported</span>,
    [ExternalServiceKind.AWSCODECOMMIT]: <span>Unsupported</span>,
    [ExternalServiceKind.OTHER]: <span>Unsupported</span>,
}

type Step = 'add-token' | 'get-ssh-key'

export const AddCredentialModal: React.FunctionComponent<AddCredentialModalProps> = ({
    onCancel,
    afterCreate,
    history,
    userID,
    externalServiceKind,
    externalServiceURL,
    requiresSSH,
    createCampaignsCredential = _createCampaignsCredential,
    initialStep = 'add-token',
}) => {
    const labelId = 'addCredential'
    const [isLoading, setIsLoading] = useState<boolean | Error>(false)
    const [credential, setCredential] = useState<string>('')
    const [sshPublicKey, setSSHPublicKey] = useState<string>()
    const [step, setStep] = useState<Step>(initialStep)
    const twoStepModal: boolean = requiresSSH

    const onChangeCredential = useCallback<React.ChangeEventHandler<HTMLInputElement>>(event => {
        setCredential(event.target.value)
    }, [])

    const onSubmit = useCallback<React.FormEventHandler>(
        async event => {
            event.preventDefault()
            setIsLoading(true)
            try {
                const createdCredential = await createCampaignsCredential({
                    user: userID,
                    credential,
                    externalServiceKind,
                    externalServiceURL,
                })
                if (twoStepModal && createdCredential.sshPublicKey) {
                    setSSHPublicKey(createdCredential.sshPublicKey)
                    setStep('get-ssh-key')
                } else {
                    afterCreate()
                }
            } catch (error) {
                setIsLoading(asError(error))
            }
        },
        [
            afterCreate,
            userID,
            credential,
            externalServiceKind,
            externalServiceURL,
            twoStepModal,
            createCampaignsCredential,
        ]
    )

    return (
        <Dialog
            className="modal-body modal-body--top-third p-4 rounded border"
            onDismiss={onCancel}
            aria-labelledby={labelId}
        >
            <div className="test-add-credential-modal">
                <h3 id={labelId}>
                    Campaigns credentials: {defaultExternalServices[externalServiceKind].defaultDisplayName}
                </h3>
                <p>
                    <strong>{externalServiceURL}</strong>
                </p>
                {twoStepModal && (
                    <div className="d-flex w-100 justify-content-between mb-4">
                        <div className="flex-grow-1 mr-2">
                            <p className={classNames('mb-0 py-2', step === 'get-ssh-key' && 'text-muted')}>
                                1. Add token
                            </p>
                            <div className="add-credential-modal__modal-step-ruler add-credential-modal__modal-step-ruler--purple" />
                        </div>
                        <div className="flex-grow-1 ml-2">
                            <p className={classNames('mb-0 py-2', step === 'add-token' && 'text-muted')}>
                                2. Get SSH Key
                            </p>
                            <div
                                className={classNames(
                                    'add-credential-modal__modal-step-ruler',
                                    step === 'add-token' && 'add-credential-modal__modal-step-ruler--gray',
                                    step === 'get-ssh-key' && 'add-credential-modal__modal-step-ruler--blue'
                                )}
                            />
                        </div>
                    </div>
                )}
                {step === 'add-token' && (
                    <>
                        {isErrorLike(isLoading) && <ErrorAlert error={isLoading} history={history} />}
                        <Form onSubmit={onSubmit}>
                            <div className="form-group">
                                <label htmlFor="token">Personal access token</label>
                                <input
                                    id="token"
                                    name="token"
                                    type="text"
                                    className="form-control test-add-credential-modal-input"
                                    required={true}
                                    minLength={1}
                                    value={credential}
                                    onChange={onChangeCredential}
                                />
                                <p className="form-text">{helpTexts[externalServiceKind]}</p>
                            </div>
                            <div className="d-flex justify-content-end">
                                <button
                                    type="button"
                                    disabled={isLoading === true}
                                    className="btn btn-outline-secondary mr-2"
                                    onClick={onCancel}
                                >
                                    Cancel
                                </button>
                                <button
                                    type="submit"
                                    disabled={isLoading === true || credential.length === 0}
                                    className="btn btn-primary test-add-credential-modal-submit"
                                >
                                    {isLoading === true && <LoadingSpinner className="icon-inline" />}
                                    {twoStepModal ? 'Next' : 'Add credential'}
                                </button>
                            </div>
                        </Form>
                    </>
                )}
                {step === 'get-ssh-key' && (
                    <>
                        <p>
                            An SSH key has been generated for your campaigns code host connection. Copy the public key
                            below and enter it on your code host.
                        </p>
                        <CodeHostSshPublicKey externalServiceKind={externalServiceKind} sshPublicKey={sshPublicKey!} />
                        <div className="d-flex justify-content-end">
                            <button type="button" className="btn btn-outline-secondary mr-2" onClick={afterCreate}>
                                Close
                            </button>
                            <button
                                type="button"
                                className="btn btn-primary test-add-credential-modal-submit"
                                onClick={afterCreate}
                            >
                                Add credential
                            </button>
                        </div>
                    </>
                )}
            </div>
        </Dialog>
    )
}
